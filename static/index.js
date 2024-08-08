const relayStatusConnecting = "Connecting...";
const relayStatusConnected = "Connected";
const relayStatusKicked = "Kicked";

const connectionStatusConnectingToRelay = "Connecting to Relay...";
const connectionStatusConnectingToObs = "Connecting to OBS on this computer...";
const connectionStatusObsClosed = "OBS connection closed";
const connectionStatusObsError = "OBS connection error";
const connectionStatusConnected = "Connected";
const connectionStatusRelayClosed = "Device connection closed";
const connectionStatusRelayError = "Device connection error";

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
        this.bridgeToDeviceBytes = 0
        this.bridgeToObsBytes = 0
        this.bitrateToDevice = 0
        this.bitrateToObs = 0
        this.prevBitrateToDeviceBytes = 0
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
        return ((this.status == connectionStatusRelayClosed)
                || (this.status == connectionStatusRelayError)
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
            this.setStatus(connectionStatusRelayError);
            this.close();
        };
        this.relayDataWebsocket.onclose = (event) => {
            this.setStatus(connectionStatusRelayError);
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
                this.bridgeToDeviceBytes += textEncoder.encode(event.data).length;
                this.relayDataWebsocket.send(event.data);
            }
        };
    }

    updateBitrates() {
        this.bitrateToDevice = 8 * (this.bridgeToDeviceBytes - this.prevBitrateToDeviceBytes)
        this.prevBitrateToDeviceBytes = this.bridgeToDeviceBytes
        this.bitrateToObs = 8 * (this.bridgeToObsBytes - this.prevBitrateToObsBytes)
        this.prevBitrateToObsBytes = this.bridgeToObsBytes
    }
}

class Relay {
    constructor() {
        this.controlWebsocket = undefined;
        this.statusUpdateTime = new Date();
        this.status = relayStatusConnecting;
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
            let connectionId = event.data;
            let connection = new Connection(connectionId);
            connection.setupRelayDataWebsocket();
            connections.unshift(connection);
            while (connections.length > 10) {
                connections.pop().close();
            }
        };
    }
}

let relay = undefined;
let connections = [];

function numberSuffix(value) {
    return (value == 1 ? "" : "s");
}

function timeAgoString(fromDate) {
    let now = new Date();
    let secondsAgo = parseInt((now.getTime() - fromDate.getTime()) / 1000);
    if (secondsAgo < 60) {
        return `${secondsAgo} second${numberSuffix(secondsAgo)} ago`;
    } else if (secondsAgo < 3600) {
        let minutesAgo = parseInt(secondsAgo / 60);
        return `${minutesAgo} minute${numberSuffix(minutesAgo)} ago`;
    } else if (secondsAgo < 86400) {
        let hoursAgo = parseInt(secondsAgo / 3600);
        return `${hoursAgo} hour${numberSuffix(hoursAgo)} ago`;
    } else {
        return fromDate.toDateString();
    }
}

function bitrateToString(bitrate) {
    if (bitrate < 1000) {
        return `${bitrate} bps`;
    } else if (bitrate < 1000000) {
        let bitrateKbps = (bitrate / 1000).toFixed(1);
        return `${bitrateKbps} kbps`;
    } else {
        let bitrateMbps = (bitrate / 1000000).toFixed(1);
        return `${bitrateMbps} Mbps`;
    }
}

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

function copyMoblinClientUrlToClipboard() {
    navigator.clipboard.writeText(`wss://mys-lang.org/obs-remote-control-relay/client/${bridgeId}`);
}

function copyObsBladeHostnameClientUrlToClipboard() {
    navigator.clipboard.writeText(`mys-lang.org/obs-remote-control-relay/client/${bridgeId}`);
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

function getTableBody(id) {
    let table = document.getElementById(id);
    while (table.rows.length > 1) {
        table.deleteRow(-1);
    }
    return table.tBodies[0];
}

function appendToRow(row, value) {
    let cell = row.insertCell(-1);
    cell.innerHTML = value;
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
        appendToRow(row, bitrateToString(connection.bitrateToDevice));
        appendToRow(row, bitrateToString(connection.bitrateToObs));
    }
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
    }, 1000);
});
