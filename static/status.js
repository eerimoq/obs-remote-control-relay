const connectionStatusConnected = "Connected";

let bridgeId = undefined;
let timerId = undefined;

class Relay {
    constructor() {
        this.websocket = undefined;
    }

    close() {
        if (this.websocket != undefined) {
            this.websocket.close();
            this.websocket = undefined;
        }
    }

    setupWebsocket() {
        this.websocket = new WebSocket(
            `wss://mys-lang.org/obs-remote-control-relay/status/${bridgeId}`);
        this.websocket.onerror = (event) => {
            reset(10000);
        };
        this.websocket.onclose = (event) => {
            reset(10000);
        };
        this.websocket.onmessage = async (event) => {
            let message = JSON.parse(event.data);
            updateConnections(message.connections);
        };
    }
}

let relay = undefined;

function reset(delayMs) {
    relay.close();
    relay = new Relay();
    if (timerId != undefined) {
        clearTimeout(timerId);
    }
    timerId = setTimeout(() => {
        timerId = undefined;
        relay.setupWebsocket();
    }, delayMs);
}

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

function updateConnections(connections) {
    let body = getTableBody('connections');
    for (const connection of connections) {
        let row = body.insertRow(-1);
        let statusWithIcon = `<i class="p-icon--spinner u-animation--spin"></i> ${connection.status}`;
        if (connection.status == connectionStatusConnected) {
            statusWithIcon = `<i class="p-icon--success"></i> ${connection.status}`;
        } else if (connection.aborted) {
            statusWithIcon = `<i class="p-icon--error"></i> ${connection.status}`;
        }
        appendToRow(row, statusWithIcon);
        appendToRow(row, timeAgoString(new Date(connection.statusUpdateTime)));
        appendToRow(row, bitrateToString(connection.bitrateToRemoteController));
        appendToRow(row, bitrateToString(connection.bitrateToObs));
    }
}
function loadbridgeId(urlParams) {
    bridgeId = urlParams.get('bridgeId');
    if (bridgeId == undefined) {
        bridgeId = crypto.randomUUID();
    }
}

window.addEventListener('DOMContentLoaded', async (event) => {
    const urlParams = new URLSearchParams(window.location.search);
    loadbridgeId(urlParams);
    relay = new Relay();
    relay.setupWebsocket();
});
