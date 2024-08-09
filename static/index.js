const relayStatusConnecting = "Connecting...";
const relayStatusConnected = "Connected";
const relayStatusKicked = "Kicked";

const connectionStatusConnectingToRelay = "Connecting to Relay...";
const connectionStatusConnectingToObs = "Connecting to OBS on this computer...";
const connectionStatusObsClosed = "OBS connection closed";
const connectionStatusObsError = "OBS connection error";
const connectionStatusConnected = "Connected";
const connectionStatusRemoteControllerClosed = "Remote controller connection closed";
const connectionStatusRemoteControllerError = "Remote controller connection error";

const defaultObsPort = "4455";
const kickedCode = 3000;

let bridgeId = undefined;
let obsPort = undefined;
let timerId = undefined;
let textEncoder = new TextEncoder();

class Connection {
    constructor(connectionId) {
        this.connectionId = connectionId;
        this.relayDataWebsocket = undefined;
        this.obsWebsocket = undefined;
        this.status = connectionStatusConnectingToRelay;
        this.statusUpdateTime = new Date();
        this.bridgeToRemoteControllerBytes = 0
        this.bridgeToObsBytes = 0
        this.bitrateToRemoteController = 0
        this.bitrateToObs = 0
        this.prevBitrateToRemoteControllerBytes = 0
        this.prevBitrateToObsBytes = 0
    }

    close() {
        if (this.relayDataWebsocket != undefined) {
            this.relayDataWebsocket.close();
        }
        if (this.obsWebsocket != undefined) {
            this.obsWebsocket.close();
        }
    }

    setStatus(newStatus) {
        if (this.status == newStatus) {
            return;
        }
        if (this.isAborted()) {
            return;
        }
        this.status = newStatus;
        this.statusUpdateTime = new Date();
        updateConnections();
    }

    isAborted() {
        return ((this.status == connectionStatusRemoteControllerClosed)
                || (this.status == connectionStatusRemoteControllerError)
                || (this.status == connectionStatusObsClosed)
                || (this.status == connectionStatusObsError));
    }

    setupRelayDataWebsocket() {
        this.relayDataWebsocket = new WebSocket(
            `wss://mys-lang.org/obs-remote-control-relay/bridge/data/${bridgeId}/${this.connectionId}`);
        this.status = connectionStatusConnectingToRelay;
        this.relayDataWebsocket.onopen = (event) => {
            this.setupObsWebsocket();
        };
        this.relayDataWebsocket.onerror = (event) => {
            this.setStatus(connectionStatusRemoteControllerError);
            this.close();
        };
        this.relayDataWebsocket.onclose = (event) => {
            this.setStatus(connectionStatusRemoteControllerError);
            this.close();
        };
        this.relayDataWebsocket.onmessage = async (event) => {
            if (this.obsWebsocket.readyState == WebSocket.OPEN) {
                this.bridgeToObsBytes += textEncoder.encode(event.data).length;
                this.obsWebsocket.send(event.data);
            }
        };
    }

    setupObsWebsocket() {
        this.obsWebsocket = new WebSocket(`ws://localhost:${obsPort}`);
        this.setStatus(connectionStatusConnectingToObs);
        this.obsWebsocket.onopen = (event) => {
            this.setStatus(connectionStatusConnected);
        };
        this.obsWebsocket.onerror = (event) => {
            this.setStatus(connectionStatusObsError);
            this.close();
        };
        this.obsWebsocket.onclose = (event) => {
            this.setStatus(connectionStatusObsClosed);
            this.close();
        };
        this.obsWebsocket.onmessage = async (event) => {
            if (this.relayDataWebsocket.readyState == WebSocket.OPEN) {
                this.bridgeToRemoteControllerBytes += textEncoder.encode(event.data).length;
                this.relayDataWebsocket.send(event.data);
            }
        };
    }

    updateBitrates() {
        this.bitrateToRemoteController = 8 * (this.bridgeToRemoteControllerBytes - this.prevBitrateToRemoteControllerBytes)
        this.prevBitrateToRemoteControllerBytes = this.bridgeToRemoteControllerBytes
        this.bitrateToObs = 8 * (this.bridgeToObsBytes - this.prevBitrateToObsBytes)
        this.prevBitrateToObsBytes = this.bridgeToObsBytes
    }
}

class Relay {
    constructor() {
        this.controlWebsocket = undefined;
        this.statusUpdateTime = new Date();
        this.status = relayStatusConnecting;
        this.statusEnabled = false;
    }

    close() {
        if (this.controlWebsocket != undefined) {
            this.controlWebsocket.close();
            this.controlWebsocket = undefined;
        }
    }

    setStatus(newStatus) {
        if (this.status == newStatus) {
            return;
        }
        this.status = newStatus;
        this.statusUpdateTime = new Date();
        updateRelayStatus();
    }

    sendStatus(status) {
        if (this.controlWebsocket != undefined && this.controlWebsocket.readyState == WebSocket.OPEN) {
            this.controlWebsocket.send(JSON.stringify(status))
        }
    }

    setupControlWebsocket() {
        this.controlWebsocket = new WebSocket(
            `wss://mys-lang.org/obs-remote-control-relay/bridge/control/${bridgeId}`);
        this.setStatus(relayStatusConnecting);
        this.controlWebsocket.onopen = (event) => {
            this.setStatus(relayStatusConnected);
        };
        this.controlWebsocket.onerror = (event) => {
            reset(10000);
        };
        this.controlWebsocket.onclose = (event) => {
            if (event.code == kickedCode) {
                this.setStatus(relayStatusKicked);
            } else {
                reset(10000);
            }
        };
        this.controlWebsocket.onmessage = async (event) => {
            let message = JSON.parse(event.data);
            if (message.type == "connect") {
                let connectionId = message.data.connectionId;
                let connection = new Connection(connectionId);
                connection.setupRelayDataWebsocket();
                connections.unshift(connection);
                while (connections.length > 5) {
                    connections.pop().close();
                }
            } else if (message.type == "startStatus") {
                this.statusEnabled = true;
            } else if (message.type == "stopStatus") {
                this.statusEnabled = false;
            }
        };
    }
}

let relay = undefined;
let connections = [];

function reset(delayMs) {
    for (const connection of connections) {
        connection.close();
    }
    connections = [];
    relay.close();
    relay = new Relay();
    if (timerId != undefined) {
        clearTimeout(timerId);
    }
    timerId = setTimeout(() => {
        timerId = undefined;
        relay.setupControlWebsocket();
    }, delayMs);
}

function copyMoblinRemoteControllerUrlToClipboard() {
    navigator.clipboard.writeText(`wss://mys-lang.org/obs-remote-control-relay/remote-controller/${bridgeId}`);
}

function copyObsBladeHostnameRemoteControllerUrlToClipboard() {
    navigator.clipboard.writeText(`mys-lang.org/obs-remote-control-relay/remote-controller/${bridgeId}`);
}

function populateObsPort() {
    document.getElementById('obsPort').value = obsPort;
}

function saveObsPort() {
    obsPort = document.getElementById('obsPort').value;
    localStorage.setItem('obsPort', obsPort);
    reset(0);
}

function resetSettings() {
    bridgeId = crypto.randomUUID();
    localStorage.setItem('bridgeId', bridgeId);
    obsPort = defaultObsPort;
    localStorage.setItem('obsPort', obsPort);
    populateObsPort();
    reset(0);
}

function updateConnections() {
    let body = getTableBody('connections');
    for (const connection of connections) {
        let row = body.insertRow(-1);
        let statusWithIcon = `<i class="p-icon--spinner u-animation--spin"></i> ${connection.status}`;
        if (connection.status == connectionStatusConnected) {
            statusWithIcon = `<i class="p-icon--success"></i> ${connection.status}`;
        } else if (connection.isAborted()) {
            statusWithIcon = `<i class="p-icon--error"></i> ${connection.status}`;
        }
        appendToRow(row, statusWithIcon);
        appendToRow(row, timeAgoString(connection.statusUpdateTime));
        appendToRow(row, bitrateToString(connection.bitrateToRemoteController));
        appendToRow(row, bitrateToString(connection.bitrateToObs));
    }
}

function updateStatus() {
    if (!relay.statusEnabled) {
        return;
    }
    let status = {
        connections: []
    };
    for (const connection of connections) {
        status.connections.push({
            status: connection.status,
            aborted: connection.isAborted(),
            statusUpdateTime: connection.statusUpdateTime,
            bitrateToRemoteController: connection.bitrateToRemoteController,
            bitrateToObs: connection.bitrateToObs
        });
    }
    relay.sendStatus(status);
}

function updateRelayStatus() {
    let statusWithIcon = `<i class="p-icon--spinner u-animation--spin"></i> ${relay.status}`;
    if (relay.status == relayStatusConnected) {
        statusWithIcon = `<i class="p-icon--success"></i> ${relay.status}`;
    } else if (relay.status == relayStatusKicked) {
        statusWithIcon = `<i class="p-icon--error"></i> ${relay.status}`;
    }
    document.getElementById('relayStatus').innerHTML = statusWithIcon;
    updateRelayStatusTimeAgo();
}

function updateRelayStatusTimeAgo() {
    document.getElementById('relayStatusTimeAgo').innerHTML = `Status changed ${timeAgoString(relay.statusUpdateTime)}.`;
}

function loadbridgeId(urlParams) {
    bridgeId = urlParams.get('bridgeId');
    if (bridgeId == undefined) {
        bridgeId = localStorage.getItem('bridgeId');
    }
    if (bridgeId == undefined) {
        bridgeId = crypto.randomUUID();
    }
    localStorage.setItem('bridgeId', bridgeId);
}

function loadObsPort(urlParams) {
    obsPort = urlParams.get('obsPort');
    if (obsPort == undefined) {
        obsPort = localStorage.getItem('obsPort');
    }
    if (obsPort == undefined) {
        obsPort = defaultObsPort;
    }
    localStorage.setItem('obsPort', obsPort);
}

window.addEventListener('DOMContentLoaded', async (event) => {
    const urlParams = new URLSearchParams(window.location.search);
    loadbridgeId(urlParams);
    loadObsPort(urlParams);
    relay = new Relay();
    relay.setupControlWebsocket();
    populateObsPort();
    updateConnections();
    updateRelayStatus();
    setInterval(() => {
        updateRelayStatusTimeAgo();
        for (const connection of connections) {
            connection.updateBitrates();
        }
        updateConnections();
        updateStatus();
    }, 1000);
});
