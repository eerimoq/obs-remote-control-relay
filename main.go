package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/puzpuzpuz/xsync/v3"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

type Connection struct {
	mutex           *sync.Mutex
	clientWebsocket *websocket.Conn
	serverWebsocket *websocket.Conn
	// Save the hello message from the server until the client connects
	helloMessageType int
	helloMessage     []byte
}

func (c *Connection) close(kicked bool) {
	c.mutex.Lock()
	if c.clientWebsocket != nil {
		c.clientWebsocket.Close()
		c.clientWebsocket = nil
	}
	if c.serverWebsocket != nil {
		if kicked {
			c.serverWebsocket.WriteControl(websocket.CloseMessage, kickData, time.Now().Add(5*time.Second))
		}
		c.serverWebsocket.Close()
		c.serverWebsocket = nil
	}
	c.mutex.Unlock()
}

var address = flag.String("address", ":8080", "HTTP server address")
var connections = xsync.NewMapOf[string, *Connection]()
var numberOfAcceptedServerWebsockets = xsync.NewCounter()
var numberOfKickedServerWebsockets = xsync.NewCounter()
var numberOfAcceptedClientWebsockets = xsync.NewCounter()
var numberOfRejectedClientWebsocketsNoServer = xsync.NewCounter()
var numberOfRejectedClientWebsocketsAlreadyInUse = xsync.NewCounter()
var numberOfServerToClientBytes = xsync.NewCounter()
var numberOfClientToServerBytes = xsync.NewCounter()
var serverToClientBitrate atomic.Int64
var clientToServerBitrate atomic.Int64
var kickData = append([]byte{0x40, 0x01}, []byte("Kicked out by other connection")...)

func serveServer(w http.ResponseWriter, r *http.Request) {
	serverWebsocket, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	connectionId := r.PathValue("connectionId")
	connection := &Connection{mutex: &sync.Mutex{}, serverWebsocket: serverWebsocket}
	numberOfAcceptedServerWebsockets.Add(1)
	connectionToClose, loaded := connections.LoadAndStore(connectionId, connection)
	if loaded {
		numberOfKickedServerWebsockets.Add(1)
		connectionToClose.close(true)
	}
	for {
		messageType, message, err := serverWebsocket.ReadMessage()
		if err != nil {
			break
		}
		numberOfServerToClientBytes.Add(int64(len(message)))
		connection.mutex.Lock()
		if connection.clientWebsocket != nil {
			connection.clientWebsocket.WriteMessage(messageType, message)
		} else {
			connection.helloMessageType = messageType
			connection.helloMessage = message
		}
		connection.mutex.Unlock()
	}
	connections.Compute(
		connectionId,
		func(oldValue *Connection, loaded bool) (*Connection, bool) {
			return oldValue, oldValue == connection
		})
	connection.close(false)
}

func serveClient(w http.ResponseWriter, r *http.Request) {
	connectionId := r.PathValue("connectionId")
	connection, ok := connections.Load(connectionId)
	if !ok {
		numberOfRejectedClientWebsocketsNoServer.Add(1)
		return
	}
	connection.mutex.Lock()
	if connection.clientWebsocket != nil {
		connection.mutex.Unlock()
		numberOfRejectedClientWebsocketsAlreadyInUse.Add(1)
		return
	}
	connection.mutex.Unlock()
	clientWebsocket, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	numberOfAcceptedClientWebsockets.Add(1)
	connection.mutex.Lock()
	if connection.serverWebsocket == nil || connection.clientWebsocket != nil {
		connection.mutex.Unlock()
		clientWebsocket.Close()
		return
	}
	connection.clientWebsocket = clientWebsocket
	serverWebsocket := connection.serverWebsocket
	if len(connection.helloMessage) != 0 {
		clientWebsocket.WriteMessage(connection.helloMessageType, connection.helloMessage)
	}
	connection.mutex.Unlock()
	for {
		messageType, message, err := clientWebsocket.ReadMessage()
		if err != nil {
			break
		}
		numberOfClientToServerBytes.Add(int64(len(message)))
		serverWebsocket.WriteMessage(messageType, message)
	}
	connection.close(false)
}

func serveStatsJson(w http.ResponseWriter, _ *http.Request) {
	statsJson := fmt.Sprintf(
		`{
	"currentNumberOfServerWebsockets": %v,
	"numberOfAcceptedServerWebsockets": %v,
	"numberOfKickedServerWebsockets": %v,
	"numberOfAcceptedClientWebsockets": %v,
	"numberOfRejectedClientWebsocketsNoServer": %v,
	"numberOfRejectedClientWebsocketsAlreadyInUse": %v,
	"numberOfServerToClientBytes": %v,
	"numberOfClientToServerBytes": %v,
	"serverToClientBitrate": %v,
	"clientToServerBitrate": %v
}`,
		connections.Size(),
		numberOfAcceptedServerWebsockets.Value(),
		numberOfKickedServerWebsockets.Value(),
		numberOfAcceptedClientWebsockets.Value(),
		numberOfRejectedClientWebsocketsNoServer.Value(),
		numberOfRejectedClientWebsocketsAlreadyInUse.Value(),
		numberOfServerToClientBytes.Value(),
		numberOfClientToServerBytes.Value(),
		serverToClientBitrate.Load(),
		clientToServerBitrate.Load())
	w.Header().Add("content-type", "application/json")
	w.Write([]byte(statsJson))
}

func updateStats() {
	var prevNumberOfServerToClientBytes int64
	var prevNumberOfClientToServerBytes int64
	for {
		newNumberOfServerToClientBytes := numberOfServerToClientBytes.Value()
		serverToClientBitrate.Store(newNumberOfServerToClientBytes - prevNumberOfServerToClientBytes)
		prevNumberOfServerToClientBytes = newNumberOfServerToClientBytes
		newNumberOfClientToServerBytes := numberOfClientToServerBytes.Value()
		clientToServerBitrate.Store(newNumberOfClientToServerBytes - prevNumberOfClientToServerBytes)
		prevNumberOfClientToServerBytes = newNumberOfClientToServerBytes
		time.Sleep(1 * time.Second)
	}
}

func main() {
	flag.Parse()
	go updateStats()
	static := http.FileServer(http.Dir("./static"))
	http.Handle("/", static)
	http.HandleFunc("/server/{connectionId}", func(w http.ResponseWriter, r *http.Request) {
		serveServer(w, r)
	})
	http.HandleFunc("/client/{connectionId}", func(w http.ResponseWriter, r *http.Request) {
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
