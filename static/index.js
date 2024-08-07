const relayStatusConnecting = "Connecting...";
const relayStatusConnected = "Connected";

const connectionStatusConnectingToRelay = "Connecting to Relay...";
const connectionStatusConnectingToObs = "Connecting to OBS on this computer...";
const connectionStatusObsClosed = "OBS connection closed";
const connectionStatusObsError = "OBS connection error";
const connectionStatusConnected = "Connected";
const connectionStatusRelayClosed = "Relay connection closed";
const connectionStatusRelayError = "Relay connection error";

const defaultObsPort = "4455";

let serverId = undefined;
let obsPort = undefined;
let relayControlWebsocket = undefined;
let relayStatusUpdateTime = new Date();
let timerId = undefined;
let relayStatus = relayStatusConnecting;

class Connection {
    constructor(connectionId) {
        this.connectionId = connectionId;
        this.relayDataWebsocket = undefined;
        this.obsWebsocket = undefined;
        this.status = connectionStatusConnectingToRelay;
        this.statusUpdateTime = new Date();
    }

    close() {
        if (this.relayDataWebsocket != undefined) {
            this.relayDataWebsocket.close();
            this.relayDataWebsocket = undefined;
        }
        if (this.obsWebsocket != undefined) {
            this.obsWebsocket.close();
            this.obsWebsocket = undefined;
        }
    }

    setStatus(newStatus) {
        if (this.status == newStatus) {
            return;
        }
        if (this.isAborted()) {
            return
        }
        this.status = newStatus;
        this.statusUpdateTime = new Date();
        updateConnections();
    }

    isAborted() {
        return ((this.status == connectionStatusRelayClosed)
                || (this.status == connectionStatusRelayError)
                || (this.status == connectionStatusObsClosed)
                || (this.status == connectionStatusObsError))
    }

    setupRelayDataWebsocket() {
        this.relayDataWebsocket = new WebSocket(
            `wss://mys-lang.org/obs-remote-control-relay/server/data/${serverId}/${this.connectionId}`);
        this.status = connectionStatusConnectingToRelay;
        this.relayDataWebsocket.onopen = (event) => {
            this.setupObsWebsocket();
        };
        this.relayDataWebsocket.onclose = (event) => {
            this.setStatus(connectionStatusRelayClosed);
            if (this.obsWebsocket != undefined) {
                this.obsWebsocket.close();
                this.obsWebsocket = undefined;
                this.relayDataWebsocket = undefined;
                this.connectionId = undefined;
            }
        };
        this.relayDataWebsocket.onclose = (event) => {
            this.setStatus(connectionStatusRelayError);
            if (this.obsWebsocket != undefined) {
                this.obsWebsocket.close();
                this.obsWebsocket = undefined;
                this.relayDataWebsocket = undefined;
                this.connectionId = undefined;
            }
        };
        this.relayDataWebsocket.onmessage = async (event) => {
            if (this.obsWebsocket != undefined) {
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
            if (this.relayDataWebsocket != undefined) {
                this.relayDataWebsocket.close()
                this.obsWebsocket = undefined;
                this.relayDataWebsocket = undefined;
                this.connectionId = undefined;
            }
        };
        this.obsWebsocket.onclose = (event) => {
            this.setStatus(connectionStatusObsClosed);
            if (this.relayDataWebsocket != undefined) {
                this.relayDataWebsocket.close()
                this.obsWebsocket = undefined;
                this.relayDataWebsocket = undefined;
                this.connectionId = undefined;
            }
        };
        this.obsWebsocket.onmessage = async (event) => {
            if (this.relayDataWebsocket != undefined) {
                this.relayDataWebsocket.send(event.data);
            }
        };
    }
}

let connections = [];

function numberSuffix(value) {
    return (value == 1 ? "" : "s")
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

function setRelayStatus(newStatus) {
    if (relayStatus == newStatus) {
        return;
    }
    relayStatus = newStatus;
    relayStatusUpdateTime = new Date();
    updateRelayStatus();
}

function reset(delayMs) {
    for (const connection of connections) {
        connection.close();
    }
    connections = [];
    setRelayStatus(relayStatusConnecting);
    if (relayControlWebsocket != undefined) {
        relayControlWebsocket.close();
        relayControlWebsocket = undefined;
    }
    if (timerId != undefined) {
        clearTimeout(timerId);
    }
    timerId = setTimeout(() => {
        timer = undefined;
        setupRelayControlWebsocket();
    }, delayMs);
}

function setupRelayControlWebsocket() {
    relayControlWebsocket = new WebSocket(
        `wss://mys-lang.org/obs-remote-control-relay/server/control/${serverId}`);
    setRelayStatus(relayStatusConnecting);
    relayControlWebsocket.onopen = (event) => {
        setRelayStatus(relayStatusConnected);
    };
    relayControlWebsocket.onclose = (event) => {
        reset(10000);
    };
    relayControlWebsocket.onmessage = async (event) => {
        connectionId = event.data;
        let connection = new Connection(connectionId);
        connection.setupRelayDataWebsocket();
        connections.unshift(connection);
        while (connections.length > 10) {
            connections.pop();
        }
    };
}

function copyMoblinClientUrlToClipboard() {
    navigator.clipboard.writeText(`wss://mys-lang.org/obs-remote-control-relay/client/${serverId}`);
}

function copyObsBladeHostnameClientUrlToClipboard() {
    navigator.clipboard.writeText(`mys-lang.org/obs-remote-control-relay/client/${serverId}`);
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
    serverId = crypto.randomUUID();
    localStorage.setItem('serverId', serverId);
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
    }
}

function formatHelp(help, kind) {
    if (help != "") {
        return ('<div class="p-notification--information">' +
                '  <div class="p-notification__content">' +
                '    <h5 class="p-notification__title">Next step</h5>' +
                `    <p class="p-notification__message">${help}</p>` +
                '  </div>' +
                '</div>');
    } else {
        return "";
    }
}

function updateRelayStatus() {
    let statusWithIcon = `<i class="p-icon--spinner u-animation--spin"></i> ${relayStatus}`;
    if (relayStatus == relayStatusConnected) {
        statusWithIcon = `<i class="p-icon--success"></i> ${relayStatus}`;
    }
    document.getElementById('relayStatus').innerHTML = statusWithIcon;
    updateRelayStatusTimeAgo();
}

function updateRelayStatusTimeAgo() {
    document.getElementById('relayStatusTimeAgo').innerHTML = `Status changed ${timeAgoString(relayStatusUpdateTime)}.`;
}

function loadServerId(urlParams) {
    serverId = urlParams.get('serverId');
    if (serverId == undefined) {
        serverId = localStorage.getItem('serverId');
    }
    if (serverId == undefined) {
        serverId = crypto.randomUUID();
    }
    localStorage.setItem('serverId', serverId);
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
    loadServerId(urlParams);
    loadObsPort(urlParams);
    setupRelayControlWebsocket();
    populateObsPort();
    updateConnections();
    updateRelayStatus();
    setInterval(() => {
        updateRelayStatusTimeAgo();
        updateConnections();
    }, 1000);
});
