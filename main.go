package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/puzpuzpuz/xsync/v3"
	"nhooyr.io/websocket"
)

type Connection struct {
	mutex               *sync.Mutex
	clientWebsocket     *websocket.Conn
	bridgeDataWebsocket *websocket.Conn
}

func (c *Connection) close() {
	c.mutex.Lock()
	if c.clientWebsocket != nil {
		c.clientWebsocket.Close(websocket.StatusGoingAway, "Closing")
		c.clientWebsocket = nil
	}
	if c.bridgeDataWebsocket != nil {
		c.bridgeDataWebsocket.Close(websocket.StatusGoingAway, "Closing")
		c.bridgeDataWebsocket = nil
	}
	c.mutex.Unlock()
}

type Bridge struct {
	mutex            *sync.Mutex
	controlWebsocket *websocket.Conn
	connections      map[string]*Connection
}

func (b *Bridge) close(kicked bool) {
	b.mutex.Lock()
	if b.controlWebsocket != nil {
		if kicked {
			b.controlWebsocket.Close(3000, "Kicked out by other connection")
		} else {
			b.controlWebsocket.Close(websocket.StatusGoingAway, "Closing")
		}
		b.controlWebsocket = nil
	}
	for _, connection := range b.connections {
		connection.close()
	}
	b.connections = make(map[string]*Connection)
	b.mutex.Unlock()
}

var address = flag.String("address", ":8080", "HTTP server address")
var bridges = xsync.NewMapOf[string, *Bridge]()
var acceptedBridgesControlWebsockets = xsync.NewCounter()
var acceptedBridgesDataWebsockets = xsync.NewCounter()
var kickedBridges = xsync.NewCounter()
var acceptedClientWebsockets = xsync.NewCounter()
var rejectedClientWebsocketsNoBridge = xsync.NewCounter()
var bridgeToClientBytes = xsync.NewCounter()
var clientToBridgeBytes = xsync.NewCounter()
var bridgeToClientBitrate atomic.Int64
var clientToBridgeBitrate atomic.Int64

func serveBridgeControl(w http.ResponseWriter, r *http.Request) {
	context := r.Context()
	bridgeControlWebsocket, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	bridgeId := r.PathValue("bridgeId")
	bridge := &Bridge{mutex: &sync.Mutex{}, controlWebsocket: bridgeControlWebsocket, connections: make(map[string]*Connection)}
	acceptedBridgesControlWebsockets.Add(1)
	bridgeToClose, loaded := bridges.LoadAndStore(bridgeId, bridge)
	if loaded {
		kickedBridges.Add(1)
		bridgeToClose.close(true)
	}
	_, _, _ = bridgeControlWebsocket.Read(context)
	bridges.Compute(
		bridgeId,
		func(oldValue *Bridge, loaded bool) (*Bridge, bool) {
			return oldValue, oldValue == bridge
		})
	bridge.close(false)
}

func serveBridgeData(w http.ResponseWriter, r *http.Request) {
	context := r.Context()
	bridgeDataWebsocket, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	bridgeDataWebsocket.SetReadLimit(-1)
	bridgeId := r.PathValue("bridgeId")
	connectionId := r.PathValue("connectionId")
	acceptedBridgesDataWebsockets.Add(1)
	bridge, ok := bridges.Load(bridgeId)
	if !ok {
		return
	}
	bridge.mutex.Lock()
	connection := bridge.connections[connectionId]
	if connection == nil {
		bridge.mutex.Unlock()
		return
	}
	connection.bridgeDataWebsocket = bridgeDataWebsocket
	bridge.mutex.Unlock()
	for {
		messageType, message, err := bridgeDataWebsocket.Read(context)
		if err != nil {
			break
		}
		bridgeToClientBytes.Add(int64(len(message)))
		connection.mutex.Lock()
		if connection.clientWebsocket != nil {
			connection.clientWebsocket.Write(context, messageType, message)
		}
		connection.mutex.Unlock()
	}
	bridge.mutex.Lock()
	delete(bridge.connections, connectionId)
	bridge.mutex.Unlock()
	connection.close()
}

func serveClient(w http.ResponseWriter, r *http.Request) {
	context := r.Context()
	bridgeId := r.PathValue("bridgeId")
	bridge, ok := bridges.Load(bridgeId)
	if !ok {
		rejectedClientWebsocketsNoBridge.Add(1)
		return
	}
	clientWebsocket, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	clientWebsocket.SetReadLimit(-1)
	acceptedClientWebsockets.Add(1)
	connectionId := uuid.New().String()
	connection := &Connection{mutex: &sync.Mutex{}, clientWebsocket: clientWebsocket}
	bridge.mutex.Lock()
	bridge.connections[connectionId] = connection
	bridge.controlWebsocket.Write(context, websocket.MessageText, []byte(connectionId))
	bridge.mutex.Unlock()
	for {
		messageType, message, err := clientWebsocket.Read(context)
		if err != nil {
			break
		}
		clientToBridgeBytes.Add(int64(len(message)))
		connection.mutex.Lock()
		if connection.bridgeDataWebsocket == nil {
			connection.mutex.Unlock()
			break
		}
		connection.bridgeDataWebsocket.Write(context, messageType, message)
		connection.mutex.Unlock()
	}
	bridge.mutex.Lock()
	delete(bridge.connections, connectionId)
	bridge.mutex.Unlock()
	connection.close()
}

func serveStatsJson(w http.ResponseWriter, _ *http.Request) {
	statsJson := fmt.Sprintf(
		`{
	"bridgesConnected": %v,
	"acceptedBridgesControlWebsockets": %v,
	"acceptedBridgesDataWebsockets": %v,
	"kickedBridges": %v,
	"acceptedClientWebsockets": %v,
	"rejectedClientWebsocketsNoBridge": %v,
	"bridgeToClientBytes": %v,
	"clientToBridgeBytes": %v,
	"bridgeToClientBitrate": %v,
	"clientToBridgeBitrate": %v
}`,
		bridges.Size(),
		acceptedBridgesControlWebsockets.Value(),
		acceptedBridgesDataWebsockets.Value(),
		kickedBridges.Value(),
		acceptedClientWebsockets.Value(),
		rejectedClientWebsocketsNoBridge.Value(),
		bridgeToClientBytes.Value(),
		clientToBridgeBytes.Value(),
		bridgeToClientBitrate.Load(),
		clientToBridgeBitrate.Load())
	w.Header().Add("content-type", "application/json")
	w.Write([]byte(statsJson))
}

func updateStats() {
	var prevBridgeToClientBytes int64
	var prevClientToBridgeBytes int64
	for {
		newBridgeToClientBytes := bridgeToClientBytes.Value()
		bridgeToClientBitrate.Store(8 * (newBridgeToClientBytes - prevBridgeToClientBytes))
		prevBridgeToClientBytes = newBridgeToClientBytes
		newClientToBridgeBytes := clientToBridgeBytes.Value()
		clientToBridgeBitrate.Store(8 * (newClientToBridgeBytes - prevClientToBridgeBytes))
		prevClientToBridgeBytes = newClientToBridgeBytes
		time.Sleep(1 * time.Second)
	}
}

func main() {
	flag.Parse()
	go updateStats()
	static := http.FileServer(http.Dir("./static"))
	http.Handle("/", static)
	http.HandleFunc("/bridge/control/{bridgeId}", func(w http.ResponseWriter, r *http.Request) {
		serveBridgeControl(w, r)
	})
	http.HandleFunc("/bridge/data/{bridgeId}/{connectionId}", func(w http.ResponseWriter, r *http.Request) {
		serveBridgeData(w, r)
	})
	http.HandleFunc("/client/{bridgeId}", func(w http.ResponseWriter, r *http.Request) {
		serveClient(w, r)
	})
	http.HandleFunc("/stats.json", func(w http.ResponseWriter, r *http.Request) {
		serveStatsJson(w, r)
	})
	err := http.ListenAndServe(*address, nil)
	if err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
}
