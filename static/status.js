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
            updateConnections([]);
            reset(10000);
        };
        this.websocket.onclose = (event) => {
            updateConnections([]);
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
