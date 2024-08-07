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
	serverDataWebsocket *websocket.Conn
}

func (c *Connection) close() {
	c.mutex.Lock()
	if c.clientWebsocket != nil {
		c.clientWebsocket.Close(websocket.StatusGoingAway, "Closing")
		c.clientWebsocket = nil
	}
	if c.serverDataWebsocket != nil {
		c.serverDataWebsocket.Close(websocket.StatusGoingAway, "Closing")
		c.serverDataWebsocket = nil
	}
	c.mutex.Unlock()
}

type Server struct {
	mutex                  *sync.Mutex
	serverControlWebsocket *websocket.Conn
	conections             map[string]*Connection
}

func (c *Server) close(kicked bool) {
	c.mutex.Lock()
	if c.serverControlWebsocket != nil {
		if kicked {
			c.serverControlWebsocket.Close(3000, "Kicked out by other connection")
		} else {
			c.serverControlWebsocket.Close(websocket.StatusGoingAway, "Closing")
		}
		c.serverControlWebsocket = nil
	}
	for _, connection := range c.conections {
		connection.close()
	}
	c.conections = make(map[string]*Connection)
	c.mutex.Unlock()
}

var address = flag.String("address", ":8080", "HTTP server address")
var servers = xsync.NewMapOf[string, *Server]()
var acceptedServerControlWebsockets = xsync.NewCounter()
var acceptedServerDataWebsockets = xsync.NewCounter()
var kickedServerWebsockets = xsync.NewCounter()
var acceptedClientWebsockets = xsync.NewCounter()
var rejectedClientWebsocketsNoServer = xsync.NewCounter()
var serverToClientBytes = xsync.NewCounter()
var clientToServerBytes = xsync.NewCounter()
var serverToClientBitrate atomic.Int64
var clientToServerBitrate atomic.Int64

func serveServerControl(w http.ResponseWriter, r *http.Request) {
	context := r.Context()
	serverControlWebsocket, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	serverId := r.PathValue("serverId")
	server := &Server{mutex: &sync.Mutex{}, serverControlWebsocket: serverControlWebsocket, conections: make(map[string]*Connection)}
	acceptedServerControlWebsockets.Add(1)
	serverToClose, loaded := servers.LoadAndStore(serverId, server)
	if loaded {
		kickedServerWebsockets.Add(1)
		serverToClose.close(true)
	}
	_, _, _ = serverControlWebsocket.Read(context)
	servers.Compute(
		serverId,
		func(oldValue *Server, loaded bool) (*Server, bool) {
			return oldValue, oldValue == server
		})
	server.close(false)
}

func serveServerData(w http.ResponseWriter, r *http.Request) {
	context := r.Context()
	serverDataWebsocket, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	serverDataWebsocket.SetReadLimit(-1)
	serverId := r.PathValue("serverId")
	connectionId := r.PathValue("connectionId")
	acceptedServerDataWebsockets.Add(1)
	server, ok := servers.Load(serverId)
	if !ok {
		return
	}
	server.mutex.Lock()
	connection := server.conections[connectionId]
	if connection == nil {
		server.mutex.Unlock()
		return
	}
	connection.serverDataWebsocket = serverDataWebsocket
	server.mutex.Unlock()
	for {
		messageType, message, err := serverDataWebsocket.Read(context)
		if err != nil {
			break
		}
		serverToClientBytes.Add(int64(len(message)))
		connection.mutex.Lock()
		if connection.clientWebsocket != nil {
			connection.clientWebsocket.Write(context, messageType, message)
		}
		connection.mutex.Unlock()
	}
	server.mutex.Lock()
	delete(server.conections, connectionId)
	server.mutex.Unlock()
	connection.close()
}

func serveClient(w http.ResponseWriter, r *http.Request) {
	context := r.Context()
	serverId := r.PathValue("serverId")
	server, ok := servers.Load(serverId)
	if !ok {
		rejectedClientWebsocketsNoServer.Add(1)
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
	server.mutex.Lock()
	server.conections[connectionId] = connection
	server.serverControlWebsocket.Write(context, websocket.MessageText, []byte(connectionId))
	server.mutex.Unlock()
	for {
		messageType, message, err := clientWebsocket.Read(context)
		if err != nil {
			break
		}
		clientToServerBytes.Add(int64(len(message)))
		connection.mutex.Lock()
		if connection.serverDataWebsocket == nil {
			connection.mutex.Unlock()
			break
		}
		connection.serverDataWebsocket.Write(context, messageType, message)
		connection.mutex.Unlock()
	}
	server.mutex.Lock()
	delete(server.conections, connectionId)
	server.mutex.Unlock()
	connection.close()
}

func serveStatsJson(w http.ResponseWriter, _ *http.Request) {
	statsJson := fmt.Sprintf(
		`{
	"serverControlWebsockets": %v,
	"acceptedServerControlWebsockets": %v,
	"acceptedServerDataWebsockets": %v,
	"kickedServerWebsockets": %v,
	"acceptedClientWebsockets": %v,
	"rejectedClientWebsocketsNoServer": %v,
	"serverToClientBytes": %v,
	"clientToServerBytes": %v,
	"serverToClientBitrate": %v,
	"clientToServerBitrate": %v
}`,
		servers.Size(),
		acceptedServerControlWebsockets.Value(),
		acceptedServerDataWebsockets.Value(),
		kickedServerWebsockets.Value(),
		acceptedClientWebsockets.Value(),
		rejectedClientWebsocketsNoServer.Value(),
		serverToClientBytes.Value(),
		clientToServerBytes.Value(),
		serverToClientBitrate.Load(),
		clientToServerBitrate.Load())
	w.Header().Add("content-type", "application/json")
	w.Write([]byte(statsJson))
}

func updateStats() {
	var prevServerToClientBytes int64
	var prevClientToServerBytes int64
	for {
		newServerToClientBytes := serverToClientBytes.Value()
		serverToClientBitrate.Store(newServerToClientBytes - prevServerToClientBytes)
		prevServerToClientBytes = newServerToClientBytes
		newClientToServerBytes := clientToServerBytes.Value()
		clientToServerBitrate.Store(newClientToServerBytes - prevClientToServerBytes)
		prevClientToServerBytes = newClientToServerBytes
		time.Sleep(1 * time.Second)
	}
}

func main() {
	flag.Parse()
	go updateStats()
	static := http.FileServer(http.Dir("./static"))
	http.Handle("/", static)
	http.HandleFunc("/server/control/{serverId}", func(w http.ResponseWriter, r *http.Request) {
		serveServerControl(w, r)
	})
	http.HandleFunc("/server/data/{serverId}/{connectionId}", func(w http.ResponseWriter, r *http.Request) {
		serveServerData(w, r)
	})
	http.HandleFunc("/client/{serverId}", func(w http.ResponseWriter, r *http.Request) {
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
