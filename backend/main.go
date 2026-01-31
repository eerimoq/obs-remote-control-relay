package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/puzpuzpuz/xsync/v3"
	"golang.org/x/time/rate"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

const controlMessageTypeConnect = "connect"
const controlMessageTypeStartStatus = "startStatus"
const controlMessageTypeStopStatus = "stopStatus"
const controlMessageTypeKicked = "kicked"
const controlMessageTypeRateLimitExceeded = "rateLimitExceeded"

type ControlMessage struct {
	Type string `json:"type"`
	Data any    `json:"data,omitempty"`
}

type ControlConnectData struct {
	ConnectionId string `json:"connectionId"`
}

type ControlRateLimitExceededData struct {
	ConnectionId string `json:"connectionId"`
}

type Connection struct {
	mutex                     *sync.Mutex
	remoteControllerWebsocket *websocket.Conn
	bridgeWebsocket           *websocket.Conn
	rateLimiter               *rate.Limiter
}

func (c *Connection) close() {
	c.mutex.Lock()
	if c.remoteControllerWebsocket != nil {
		remoteControllersConnected.Dec()
		c.remoteControllerWebsocket.Close(websocket.StatusGoingAway, "")
		c.remoteControllerWebsocket = nil
		logger.Debug("closed remote controller websocket in connection")
	}
	if c.bridgeWebsocket != nil {
		bridgeRemoteControllersConnected.Dec()
		c.bridgeWebsocket.Close(websocket.StatusGoingAway, "")
		c.bridgeWebsocket = nil
		logger.Debug("closed bridge websocket in connection")
	}
	c.mutex.Unlock()
}

type Bridge struct {
	mutex            *sync.Mutex
	controlWebsocket *websocket.Conn
	connections      map[string]*Connection
	statusWebsockets map[*websocket.Conn]bool
}

func (b *Bridge) close(kicked bool) {
	b.mutex.Lock()
	if b.controlWebsocket != nil {
		if kicked {
			wsjson.Write(context.Background(),
				b.controlWebsocket,
				ControlMessage{
					Type: controlMessageTypeKicked,
				})
			logger.Info("sent kicked control message to bridge before closing")
		}
		b.controlWebsocket.Close(websocket.StatusGoingAway, "")
		b.controlWebsocket = nil
		logger.Debug("closed bridge control websocket")
	}
	for _, connection := range b.connections {
		connection.close()
	}
	b.connections = make(map[string]*Connection)
	for statusWebsocket := range b.statusWebsockets {
		statusWebsocket.Close(websocket.StatusAbnormalClosure, "")
	}
	b.connections = make(map[string]*Connection)
	b.mutex.Unlock()
}

var address = flag.String("address", ":8080", "HTTP server address")
var reverseProxyBase = flag.String("reverse_proxy_base", "", "Reverse proxy base (default: \"\")")

var bridges = xsync.NewMapOf[string, *Bridge]()
var startTime = time.Now()
var bridgeRemoteControllersConnected = xsync.NewCounter()
var remoteControllersConnected = xsync.NewCounter()
var bridgeToRemoteControllerBytes = xsync.NewCounter()
var remoteControllerToBridgeBytes = xsync.NewCounter()
var rateLimitExceeded = xsync.NewCounter()
var bridgeToRemoteControllerBitrate atomic.Int64
var remoteControllerToBridgeBitrate atomic.Int64
var logger *slog.Logger

var websocketSubprotocols = [1]string{"obswebsocket.json"}
var websocketAcceptOptions = &websocket.AcceptOptions{
	Subprotocols:       websocketSubprotocols[:],
	InsecureSkipVerify: true,
}

func serveBridgeControl(w http.ResponseWriter, r *http.Request) {
	context := r.Context()
	bridgeControlWebsocket, err := websocket.Accept(w, r, nil)
	if err != nil {
		logger.Error("failed to accept bridge control websocket", "error", err.Error())
		return
	}
	bridgeId := r.PathValue("bridgeId")
	logger.Info("bridge control websocket accepted", "bridgeId", bridgeId)
	bridge := &Bridge{
		mutex:            &sync.Mutex{},
		controlWebsocket: bridgeControlWebsocket,
		connections:      make(map[string]*Connection),
		statusWebsockets: make(map[*websocket.Conn]bool),
	}
	bridgeToClose, loaded := bridges.LoadAndStore(bridgeId, bridge)
	if loaded {
		logger.Info("replacing existing bridge", "bridgeId", bridgeId)
		bridgeToClose.close(true)
	}
	for {
		messageType, message, err := bridgeControlWebsocket.Read(context)
		if err != nil {
			logger.Debug("bridge control read loop ended", "bridgeId", bridgeId, "error", err.Error())
			break
		}
		bridge.mutex.Lock()
		for statusWebsocket := range bridge.statusWebsockets {
			statusWebsocket.Write(context, messageType, message)
		}
		bridge.mutex.Unlock()
	}
	bridges.Compute(
		bridgeId,
		func(oldValue *Bridge, loaded bool) (*Bridge, bool) {
			return oldValue, oldValue == bridge
		})
	logger.Info("closing bridge control", "bridgeId", bridgeId)
	bridge.close(false)
}

func handleRateLimitExceeded(bridgeControlWebsocket *websocket.Conn, connectionId string) {
	rateLimitExceeded.Inc()
	logger.Warn("rate limit exceeded", "connectionId", connectionId)
	wsjson.Write(context.Background(),
		bridgeControlWebsocket,
		ControlMessage{
			Type: controlMessageTypeRateLimitExceeded,
			Data: ControlRateLimitExceededData{
				ConnectionId: connectionId,
			},
		})
}

func serveBridgeData(w http.ResponseWriter, r *http.Request) {
	context := r.Context()
	bridgeWebsocket, err := websocket.Accept(w, r, nil)
	if err != nil {
		logger.Error("failed to accept bridge data websocket", "error", err.Error())
		return
	}
	bridgeWebsocket.SetReadLimit(-1)
	bridgeId := r.PathValue("bridgeId")
	connectionId := r.PathValue("connectionId")
	logger.Info("bridge data websocket accepted", "bridgeId", bridgeId, "connectionId", connectionId)
	bridge, ok := bridges.Load(bridgeId)
	if !ok {
		logger.Warn("bridge not found for data connection", "bridgeId", bridgeId, "connectionId", connectionId)
		bridgeWebsocket.Close(websocket.StatusGoingAway, "")
		return
	}
	bridge.mutex.Lock()
	connection := bridge.connections[connectionId]
	if connection == nil {
		bridge.mutex.Unlock()
		logger.Warn("connection not found for bridge data", "bridgeId", bridgeId, "connectionId", connectionId)
		bridgeWebsocket.Close(websocket.StatusGoingAway, "")
		return
	}
	bridgeControlWebsocket := bridge.controlWebsocket
	connection.mutex.Lock()
	connection.bridgeWebsocket = bridgeWebsocket
	rateLimiter := connection.rateLimiter
	connection.mutex.Unlock()
	bridge.mutex.Unlock()
	bridgeRemoteControllersConnected.Inc()
	for {
		messageType, message, err := bridgeWebsocket.Read(context)
		if err != nil {
			logger.Debug("bridge data read loop ended", "bridgeId", bridgeId, "connectionId", connectionId, "error", err.Error())
			break
		}
		length := len(message)
		bridgeToRemoteControllerBytes.Add(int64(length))
		if !rateLimiter.AllowN(time.Now(), 8*length) {
			handleRateLimitExceeded(bridgeControlWebsocket, connectionId)
			break
		}
		connection.mutex.Lock()
		if connection.remoteControllerWebsocket != nil {
			connection.remoteControllerWebsocket.Write(context, messageType, message)
		}
		connection.mutex.Unlock()
	}
	bridge.mutex.Lock()
	delete(bridge.connections, connectionId)
	bridge.mutex.Unlock()
	logger.Info("bridge data connection closed", "bridgeId", bridgeId, "connectionId", connectionId)
	connection.close()
}

func serveRemoteController(w http.ResponseWriter, r *http.Request) {
	context := r.Context()
	bridgeId := r.PathValue("bridgeId")
	bridge, ok := bridges.Load(bridgeId)
	if !ok {
		logger.Warn("bridge not found for remote controller", "bridgeId", bridgeId)
		return
	}
	remoteControllerWebsocket, err := websocket.Accept(w, r, websocketAcceptOptions)
	if err != nil {
		logger.Error("failed to accept remote controller websocket", "bridgeId", bridgeId, "error", err.Error())
		return
	}
	remoteControllerWebsocket.SetReadLimit(-1)
	connectionId := uuid.New().String()
	logger.Info("remote controller connected", "bridgeId", bridgeId, "connectionId", connectionId)
	// Average 0.5 Mbps, burst 10 Mbps.
	rateLimiter := rate.NewLimiter(rate.Every(time.Microsecond)/2, 10000000)
	connection := &Connection{
		mutex:                     &sync.Mutex{},
		remoteControllerWebsocket: remoteControllerWebsocket,
		rateLimiter:               rateLimiter,
	}
	remoteControllersConnected.Inc()
	bridge.mutex.Lock()
	bridge.connections[connectionId] = connection
	bridgeControlWebsocket := bridge.controlWebsocket
	wsjson.Write(context, bridgeControlWebsocket, ControlMessage{
		Type: controlMessageTypeConnect,
		Data: ControlConnectData{
			ConnectionId: connectionId,
		},
	})
	bridge.mutex.Unlock()
	for {
		messageType, message, err := remoteControllerWebsocket.Read(context)
		if err != nil {
			logger.Debug("remote controller read loop ended", "bridgeId", bridgeId, "connectionId", connectionId, "error", err.Error())
			break
		}
		length := len(message)
		remoteControllerToBridgeBytes.Add(int64(length))
		if !rateLimiter.AllowN(time.Now(), 8*length) {
			handleRateLimitExceeded(bridgeControlWebsocket, connectionId)
			break
		}
		connection.mutex.Lock()
		if connection.bridgeWebsocket == nil {
			connection.mutex.Unlock()
			logger.Debug("remote controller bridge websocket nil, ending loop", "bridgeId", bridgeId, "connectionId", connectionId)
			break
		}
		connection.bridgeWebsocket.Write(context, messageType, message)
		connection.mutex.Unlock()
	}
	bridge.mutex.Lock()
	delete(bridge.connections, connectionId)
	bridge.mutex.Unlock()
	logger.Info("remote controller disconnected", "bridgeId", bridgeId, "connectionId", connectionId)
	connection.close()
}

func serveStatus(w http.ResponseWriter, r *http.Request) {
	context := r.Context()
	bridgeId := r.PathValue("bridgeId")
	bridge, ok := bridges.Load(bridgeId)
	if !ok {
		logger.Warn("bridge not found for status", "bridgeId", bridgeId)
		return
	}
	statusWebsocket, err := websocket.Accept(w, r, nil)
	if err != nil {
		logger.Error("failed to accept status websocket", "bridgeId", bridgeId, "error", err.Error())
		return
	}
	statusWebsocket.SetReadLimit(-1)
	logger.Info("status websocket accepted", "bridgeId", bridgeId)
	bridge.mutex.Lock()
	if len(bridge.statusWebsockets) == 0 {
		if bridge.controlWebsocket == nil {
			bridge.mutex.Unlock()
			return
		}
		wsjson.Write(context, bridge.controlWebsocket, ControlMessage{
			Type: controlMessageTypeStartStatus,
		})
		logger.Debug("sent startStatus to bridge control", "bridgeId", bridgeId)
	}
	bridge.statusWebsockets[statusWebsocket] = true
	bridge.mutex.Unlock()
	_, _, _ = statusWebsocket.Read(context)
	bridge.mutex.Lock()
	delete(bridge.statusWebsockets, statusWebsocket)
	if len(bridge.statusWebsockets) == 0 {
		if bridge.controlWebsocket != nil {
			wsjson.Write(context, bridge.controlWebsocket, ControlMessage{
				Type: controlMessageTypeStopStatus,
			})
			logger.Debug("sent stopStatus to bridge control", "bridgeId", bridgeId)
		}
	}
	bridge.mutex.Unlock()
	logger.Info("status websocket closed", "bridgeId", bridgeId)
	statusWebsocket.Close(websocket.StatusAbnormalClosure, "")
}

type StatsGeneral struct {
	StartTime         int64 `json:"startTime"`
	RateLimitExceeded int64 `json:"rateLimitExceeded"`
}

type StatsTrafficDirection struct {
	TotalBytes     int64 `json:"totalBytes"`
	CurrentBitrate int64 `json:"currentBitrate"`
}

type StatsBridges struct {
	Connected                  int   `json:"connected"`
	RemoteControllersConnected int64 `json:"remoteControllersConnected"`
}

type StatsRemoteControllers struct {
	Connected int64 `json:"connected"`
}

type StatsTraffic struct {
	BridgesToRemoteControllers StatsTrafficDirection `json:"bridgesToRemoteControllers"`
	RemoteControllersToBridges StatsTrafficDirection `json:"remoteControllersToBridges"`
}

type Stats struct {
	General           StatsGeneral           `json:"general"`
	Bridges           StatsBridges           `json:"bridges"`
	RemoteControllers StatsRemoteControllers `json:"remoteControllers"`
	Traffic           StatsTraffic           `json:"traffic"`
}

func serveStatsJson(w http.ResponseWriter, _ *http.Request) {
	logger.Debug("stats.json requested")
	stats := Stats{
		General: StatsGeneral{
			StartTime:         startTime.Unix(),
			RateLimitExceeded: rateLimitExceeded.Value(),
		},
		Bridges: StatsBridges{
			Connected:                  bridges.Size(),
			RemoteControllersConnected: bridgeRemoteControllersConnected.Value(),
		},
		RemoteControllers: StatsRemoteControllers{
			Connected: remoteControllersConnected.Value(),
		},
		Traffic: StatsTraffic{
			BridgesToRemoteControllers: StatsTrafficDirection{
				TotalBytes:     bridgeToRemoteControllerBytes.Value(),
				CurrentBitrate: bridgeToRemoteControllerBitrate.Load(),
			},
			RemoteControllersToBridges: StatsTrafficDirection{
				TotalBytes:     remoteControllerToBridgeBytes.Value(),
				CurrentBitrate: remoteControllerToBridgeBitrate.Load(),
			},
		},
	}
	statsJson, err := json.Marshal(stats)
	if err != nil {
		return
	}
	w.Header().Add("content-type", "application/json")
	w.Write(statsJson)
}

func updateStats() {
	logger.Debug("stats bitrate updater started")
	var prevBridgeToRemoteControllerBytes int64
	var prevRemoteControllerToBridgeBytes int64
	for {
		newBridgeToRemoteControllerBytes := bridgeToRemoteControllerBytes.Value()
		bridgeToRemoteControllerBitrate.Store(8 * (newBridgeToRemoteControllerBytes - prevBridgeToRemoteControllerBytes))
		prevBridgeToRemoteControllerBytes = newBridgeToRemoteControllerBytes
		newRemoteControllerToBridgeBytes := remoteControllerToBridgeBytes.Value()
		remoteControllerToBridgeBitrate.Store(8 * (newRemoteControllerToBridgeBytes - prevRemoteControllerToBridgeBytes))
		prevRemoteControllerToBridgeBytes = newRemoteControllerToBridgeBytes
		time.Sleep(1 * time.Second)
	}
}

func serveConfigJs(w http.ResponseWriter, _ *http.Request) {
	logger.Debug("config.mjs requested")
	configJs := fmt.Sprintf("export const baseUrl = `${window.location.host}%v`;", *reverseProxyBase)
	w.Header().Add("content-type", "text/javascript")
	w.Write([]byte(configJs))
}

func setLogLevel() {
	level := slog.LevelInfo
	switch strings.ToUpper(strings.TrimSpace(os.Getenv("LOG_LEVEL"))) {
	case "DEBUG":
		level = slog.LevelDebug
	case "INFO", "":
		level = slog.LevelInfo
	case "WARN", "WARNING":
		level = slog.LevelWarn
	case "ERROR":
		level = slog.LevelError
	}
	logger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}

func main() {
	flag.Parse()
	setLogLevel()
	logger.Info("server starting", "address", *address, "reverse_proxy_base", *reverseProxyBase)
	go updateStats()
	static := http.FileServer(http.Dir("../frontend"))
	http.Handle("/", static)
	http.HandleFunc("/bridge/control/{bridgeId}", func(w http.ResponseWriter, r *http.Request) {
		serveBridgeControl(w, r)
	})
	http.HandleFunc("/bridge/data/{bridgeId}/{connectionId}", func(w http.ResponseWriter, r *http.Request) {
		serveBridgeData(w, r)
	})
	http.HandleFunc("/remote-controller/{bridgeId}", func(w http.ResponseWriter, r *http.Request) {
		serveRemoteController(w, r)
	})
	http.HandleFunc("/status/{bridgeId}", func(w http.ResponseWriter, r *http.Request) {
		serveStatus(w, r)
	})
	http.HandleFunc("/js/config.mjs", func(w http.ResponseWriter, r *http.Request) {
		serveConfigJs(w, r)
	})
	http.HandleFunc("/stats.json", func(w http.ResponseWriter, r *http.Request) {
		serveStatsJson(w, r)
	})
	logger.Info("listening for HTTP requests", "address", *address)
	err := http.ListenAndServe(*address, nil)
	if err != nil {
		logger.Error("ListenAndServe failed", "error", err.Error())
	}
}
