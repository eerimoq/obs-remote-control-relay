package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
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

type ControlMessage struct {
	Type string `json:"type"`
	Data any    `json:"data,omitempty"`
}

type ControlConnectData struct {
	ConnectionId string `json:"connectionId"`
}

type Connection struct {
	mutex                     *sync.Mutex
	remoteControllerWebsocket *websocket.Conn
	bridgeWebsocket           *websocket.Conn
	rateLimiter               *rate.Limiter
}

func (c *Connection) close(code websocket.StatusCode, reason string) {
	c.mutex.Lock()
	if c.remoteControllerWebsocket != nil {
		remoteControllersConnected.Dec()
		c.remoteControllerWebsocket.Close(code, reason)
		c.remoteControllerWebsocket = nil
	}
	if c.bridgeWebsocket != nil {
		bridgeRemoteControllersConnected.Dec()
		c.bridgeWebsocket.Close(code, reason)
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
			b.controlWebsocket.Close(3000, "Kicked out by other bridge")
		} else {
			b.controlWebsocket.Close(websocket.StatusGoingAway, "")
		}
		b.controlWebsocket = nil
	}
	for _, connection := range b.connections {
		connection.close(websocket.StatusGoingAway, "")
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

func serveBridgeControl(w http.ResponseWriter, r *http.Request) {
	context := r.Context()
	bridgeControlWebsocket, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	bridgeId := r.PathValue("bridgeId")
	bridge := &Bridge{
		mutex:            &sync.Mutex{},
		controlWebsocket: bridgeControlWebsocket,
		connections:      make(map[string]*Connection),
		statusWebsockets: make(map[*websocket.Conn]bool),
	}
	bridgeToClose, loaded := bridges.LoadAndStore(bridgeId, bridge)
	if loaded {
		bridgeToClose.close(true)
	}
	for {
		messageType, message, err := bridgeControlWebsocket.Read(context)
		if err != nil {
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
	bridge.close(false)
}

func serveBridgeData(w http.ResponseWriter, r *http.Request) {
	context := r.Context()
	bridgeWebsocket, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	bridgeWebsocket.SetReadLimit(-1)
	bridgeId := r.PathValue("bridgeId")
	connectionId := r.PathValue("connectionId")
	bridge, ok := bridges.Load(bridgeId)
	if !ok {
		bridgeWebsocket.Close(websocket.StatusGoingAway, "")
		return
	}
	bridge.mutex.Lock()
	connection := bridge.connections[connectionId]
	if connection == nil {
		bridge.mutex.Unlock()
		bridgeWebsocket.Close(websocket.StatusGoingAway, "")
		return
	}
	connection.mutex.Lock()
	connection.bridgeWebsocket = bridgeWebsocket
	rateLimiter := connection.rateLimiter
	connection.mutex.Unlock()
	bridge.mutex.Unlock()
	bridgeRemoteControllersConnected.Inc()
	code := websocket.StatusGoingAway
	reason := ""
	for {
		messageType, message, err := bridgeWebsocket.Read(context)
		if err != nil {
			break
		}
		length := len(message)
		bridgeToRemoteControllerBytes.Add(int64(length))
		if !rateLimiter.AllowN(time.Now(), 8*length) {
			rateLimitExceeded.Inc()
			code = 3001
			reason = "Rate limit exceeded"
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
	connection.close(code, reason)
}

func serveRemoteController(w http.ResponseWriter, r *http.Request) {
	context := r.Context()
	bridgeId := r.PathValue("bridgeId")
	bridge, ok := bridges.Load(bridgeId)
	if !ok {
		return
	}
	remoteControllerWebsocket, err := websocket.Accept(w, r, nil)
	if err != nil {
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
	bridge.mutex.Lock()
	bridge.connections[connectionId] = connection
	wsjson.Write(context, bridge.controlWebsocket, ControlMessage{
		Type: controlMessageTypeConnect,
		Data: ControlConnectData{
			ConnectionId: connectionId,
		},
	})
	bridge.mutex.Unlock()
	code := websocket.StatusGoingAway
	reason := ""
	for {
		messageType, message, err := remoteControllerWebsocket.Read(context)
		if err != nil {
			break
		}
		length := len(message)
		remoteControllerToBridgeBytes.Add(int64(length))
		if !rateLimiter.AllowN(time.Now(), 8*length) {
			rateLimitExceeded.Inc()
			code = 3001
			reason = "Rate limit exceeded"
			break
		}
		connection.mutex.Lock()
		if connection.bridgeWebsocket == nil {
			connection.mutex.Unlock()
			break
		}
		connection.bridgeWebsocket.Write(context, messageType, message)
		connection.mutex.Unlock()
	}
	bridge.mutex.Lock()
	delete(bridge.connections, connectionId)
	bridge.mutex.Unlock()
	connection.close(code, reason)
}

func serveStatus(w http.ResponseWriter, r *http.Request) {
	context := r.Context()
	bridgeId := r.PathValue("bridgeId")
	bridge, ok := bridges.Load(bridgeId)
	if !ok {
		return
	}
	statusWebsocket, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	statusWebsocket.SetReadLimit(-1)
	bridge.mutex.Lock()
	if len(bridge.statusWebsockets) == 0 {
		if bridge.controlWebsocket == nil {
			return
		}
		wsjson.Write(context, bridge.controlWebsocket, ControlMessage{
			Type: controlMessageTypeStartStatus,
		})
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
		}
	}
	bridge.mutex.Unlock()
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
	configJs := fmt.Sprintf("const baseUrl = `${window.location.host}%v`;", *reverseProxyBase)
	w.Header().Add("content-type", "text/javascript")
	w.Write([]byte(configJs))
}

func main() {
	flag.Parse()
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
	http.HandleFunc("/config.js", func(w http.ResponseWriter, r *http.Request) {
		serveConfigJs(w, r)
	})
	http.HandleFunc("/stats.json", func(w http.ResponseWriter, r *http.Request) {
		serveStatsJson(w, r)
	})
	err := http.ListenAndServe(*address, nil)
	if err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
}
