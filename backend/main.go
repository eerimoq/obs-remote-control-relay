package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
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

// Log level constants (higher = more verbose)
const (
	levelError = iota
	levelWarn
	levelInfo
	levelDebug
)

var levelNames = map[int]string{
	levelError: "ERROR",
	levelWarn:  "WARN",
	levelInfo:  "INFO",
	levelDebug: "DEBUG",
}

type appLogger struct {
	level int
	log   *log.Logger
}

func parseLogLevel(s string) int {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "DEBUG":
		return levelDebug
	case "INFO", "":
		return levelInfo
	case "WARN", "WARNING":
		return levelWarn
	case "ERROR":
		return levelError
	default:
		return levelInfo
	}
}

func (l *appLogger) enabled(level int) bool { return level <= l.level }

func (l *appLogger) logLine(level int, msg string, keysAndValues ...any) {
	if !l.enabled(level) {
		return
	}
	ts := time.Now().UTC().Format(time.RFC3339)
	line := fmt.Sprintf("%s %-5s %s", ts, levelNames[level], msg)
	for i := 0; i+1 < len(keysAndValues); i += 2 {
		line += fmt.Sprintf(" %v=%v", keysAndValues[i], keysAndValues[i+1])
	}
	l.log.Output(3, line)
}

func (l *appLogger) Debug(msg string, keysAndValues ...any) { l.logLine(levelDebug, msg, keysAndValues...) }
func (l *appLogger) Info(msg string, keysAndValues ...any)  { l.logLine(levelInfo, msg, keysAndValues...) }
func (l *appLogger) Warn(msg string, keysAndValues ...any)  { l.logLine(levelWarn, msg, keysAndValues...) }
func (l *appLogger) Error(msg string, keysAndValues ...any) { l.logLine(levelError, msg, keysAndValues...) }

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
	}
	if c.bridgeWebsocket != nil {
		bridgeRemoteControllersConnected.Dec()
		c.bridgeWebsocket.Close(websocket.StatusGoingAway, "")
		c.bridgeWebsocket = nil
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
		}
		b.controlWebsocket.Close(websocket.StatusGoingAway, "")
		b.controlWebsocket = nil
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

var websocketSubprotocols = [1]string{"obswebsocket.json"}
var websocketAcceptOptions = &websocket.AcceptOptions{
	Subprotocols:       websocketSubprotocols[:],
	InsecureSkipVerify: true,
}

var logger *appLogger

func serveBridgeControl(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	bridgeId := r.PathValue("bridgeId")
	bridgeControlWebsocket, err := websocket.Accept(w, r, nil)
	if err != nil {
		logger.Warn("bridge control websocket accept failed", "bridgeId", bridgeId, "error", err)
		return
	}
	logger.Info("bridge control connected", "bridgeId", bridgeId)
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
		messageType, message, err := bridgeControlWebsocket.Read(ctx)
		if err != nil {
			logger.Debug("bridge control read ended", "bridgeId", bridgeId, "error", err)
			break
		}
		bridge.mutex.Lock()
		for statusWebsocket := range bridge.statusWebsockets {
			statusWebsocket.Write(ctx, messageType, message)
		}
		bridge.mutex.Unlock()
	}
	bridges.Compute(
		bridgeId,
		func(oldValue *Bridge, loaded bool) (*Bridge, bool) {
			return oldValue, oldValue == bridge
		})
	bridge.close(false)
	logger.Info("bridge control disconnected", "bridgeId", bridgeId)
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
	ctx := r.Context()
	bridgeId := r.PathValue("bridgeId")
	connectionId := r.PathValue("connectionId")
	bridgeWebsocket, err := websocket.Accept(w, r, nil)
	if err != nil {
		logger.Warn("bridge data websocket accept failed", "bridgeId", bridgeId, "connectionId", connectionId, "error", err)
		return
	}
	bridgeWebsocket.SetReadLimit(-1)
	bridge, ok := bridges.Load(bridgeId)
	if !ok {
		logger.Warn("bridge data rejected: bridge not found", "bridgeId", bridgeId, "connectionId", connectionId)
		bridgeWebsocket.Close(websocket.StatusGoingAway, "")
		return
	}
	bridge.mutex.Lock()
	connection := bridge.connections[connectionId]
	if connection == nil {
		bridge.mutex.Unlock()
		logger.Warn("bridge data rejected: connection not found", "bridgeId", bridgeId, "connectionId", connectionId)
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
	logger.Info("bridge data connected", "bridgeId", bridgeId, "connectionId", connectionId)
	for {
		messageType, message, err := bridgeWebsocket.Read(ctx)
		if err != nil {
			logger.Debug("bridge data read ended", "bridgeId", bridgeId, "connectionId", connectionId, "error", err)
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
			connection.remoteControllerWebsocket.Write(ctx, messageType, message)
		}
		connection.mutex.Unlock()
	}
	bridge.mutex.Lock()
	delete(bridge.connections, connectionId)
	bridge.mutex.Unlock()
	connection.close()
	logger.Info("bridge data disconnected", "bridgeId", bridgeId, "connectionId", connectionId)
}

func serveRemoteController(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	bridgeId := r.PathValue("bridgeId")
	bridge, ok := bridges.Load(bridgeId)
	if !ok {
		logger.Warn("remote controller rejected: bridge not found", "bridgeId", bridgeId)
		return
	}
	remoteControllerWebsocket, err := websocket.Accept(w, r, websocketAcceptOptions)
	if err != nil {
		logger.Warn("remote controller websocket accept failed", "bridgeId", bridgeId, "error", err)
		return
	}
	remoteControllerWebsocket.SetReadLimit(-1)
	connectionId := uuid.New().String()
	// Average 0.5 Mbps, burst 10 Mbps.
	rateLimiter := rate.NewLimiter(rate.Every(time.Microsecond)/2, 10000000)
	connection := &Connection{
		mutex:                     &sync.Mutex{},
		remoteControllerWebsocket: remoteControllerWebsocket,
		rateLimiter:               rateLimiter,
	}
	remoteControllersConnected.Inc()
	logger.Info("remote controller connected", "bridgeId", bridgeId, "connectionId", connectionId)
	bridge.mutex.Lock()
	bridge.connections[connectionId] = connection
	bridgeControlWebsocket := bridge.controlWebsocket
	wsjson.Write(ctx, bridgeControlWebsocket, ControlMessage{
		Type: controlMessageTypeConnect,
		Data: ControlConnectData{
			ConnectionId: connectionId,
		},
	})
	bridge.mutex.Unlock()
	for {
		messageType, message, err := remoteControllerWebsocket.Read(ctx)
		if err != nil {
			logger.Debug("remote controller read ended", "bridgeId", bridgeId, "connectionId", connectionId, "error", err)
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
			break
		}
		connection.bridgeWebsocket.Write(ctx, messageType, message)
		connection.mutex.Unlock()
	}
	bridge.mutex.Lock()
	delete(bridge.connections, connectionId)
	bridge.mutex.Unlock()
	connection.close()
	logger.Info("remote controller disconnected", "bridgeId", bridgeId, "connectionId", connectionId)
}

func serveStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	bridgeId := r.PathValue("bridgeId")
	bridge, ok := bridges.Load(bridgeId)
	if !ok {
		logger.Warn("status websocket rejected: bridge not found", "bridgeId", bridgeId)
		return
	}
	statusWebsocket, err := websocket.Accept(w, r, nil)
	if err != nil {
		logger.Warn("status websocket accept failed", "bridgeId", bridgeId, "error", err)
		return
	}
	statusWebsocket.SetReadLimit(-1)
	bridge.mutex.Lock()
	if len(bridge.statusWebsockets) == 0 {
		if bridge.controlWebsocket == nil {
			bridge.mutex.Unlock()
			return
		}
		wsjson.Write(ctx, bridge.controlWebsocket, ControlMessage{
			Type: controlMessageTypeStartStatus,
		})
	}
	bridge.statusWebsockets[statusWebsocket] = true
	bridge.mutex.Unlock()
	logger.Debug("status websocket connected", "bridgeId", bridgeId)
	_, _, _ = statusWebsocket.Read(ctx)
	bridge.mutex.Lock()
	delete(bridge.statusWebsockets, statusWebsocket)
	if len(bridge.statusWebsockets) == 0 {
		if bridge.controlWebsocket != nil {
			wsjson.Write(ctx, bridge.controlWebsocket, ControlMessage{
				Type: controlMessageTypeStopStatus,
			})
		}
	}
	bridge.mutex.Unlock()
	statusWebsocket.Close(websocket.StatusAbnormalClosure, "")
	logger.Debug("status websocket disconnected", "bridgeId", bridgeId)
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

func serveStatsJson(w http.ResponseWriter, r *http.Request) {
	logger.Debug("stats requested", "remoteAddr", r.RemoteAddr)
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
		logger.Error("failed to marshal stats", "error", err)
		return
	}
	w.Header().Add("content-type", "application/json")
	w.Write(statsJson)
}

func updateStats() {
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
	configJs := fmt.Sprintf("export const baseUrl = `${window.location.host}%v`;", *reverseProxyBase)
	w.Header().Add("content-type", "text/javascript")
	w.Write([]byte(configJs))
}

func main() {
	flag.Parse()
	logLevel := parseLogLevel(os.Getenv("LOG_LEVEL"))
	logger = &appLogger{
		level: logLevel,
		log:   log.New(os.Stdout, "", 0),
	}
	logger.Info("starting OBS remote control relay",
		"address", *address,
		"reverseProxyBase", *reverseProxyBase,
		"logLevel", levelNames[logLevel])
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
	logger.Info("HTTP server listening", "address", *address)
	err := http.ListenAndServe(*address, nil)
	if err != nil {
		logger.Error("HTTP server failed", "error", err)
		log.Fatal("ListenAndServe: ", err)
	}
}
