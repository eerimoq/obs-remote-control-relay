const statusConnectingToObs = "Connecting to OBS on this computer. It may take up to a minute...";
const statusConnectingToRelay = "Connecting to Relay...";
const statusWaitingForStreamingDevice = "Waiting for the streaming device to connect...";
const statusStreamingDeviceConnected = "Streaming device connected to OBS! Enjoy your stream!";
const statusObsError = "OBS connection error. Aborting...";
const statusObsAuthDisabledError = "OBS WebSocket Server authentication disabled. Aborting...";
const statusKickedOut = "Kicked out. Aborting...";

let connectionId = undefined;
let obsPort = undefined;
let relayWebsocket = undefined;
let obsWebsocket = undefined;
let timerId = undefined;
let status = statusConnectingToObs;
let helloMessage = undefined;
let relayConnected = false;

const closeCodeReUsedConnectionId = 0x4001

function setStatus(newStatus) {
    if (status == newStatus) {
        return;
    }
    // console.log(`State change ${status} -> ${newStatus}`)
    status = newStatus;
    updateStatus();
}

function reset(delayMs) {
    helloMessage = undefined;
    setStatus(statusConnectingToObs);
    if (relayWebsocket != undefined) {
        relayWebsocket.close();
        relayWebsocket = undefined;
        relayConnected = false;
    }
    if (obsWebsocket != undefined) {
        obsWebsocket.close();
        obsWebsocket = undefined;
    }
    if (timerId != undefined) {
        clearTimeout(timerId);
    }
    timerId = setTimeout(() => {
        timer = undefined;
        setupObsWebsocket();
    }, delayMs);
}

function setupRelayWebsocket() {
    relayWebsocket = new WebSocket(
        `wss://mys-lang.org/obs-remote-control-relay/server/${connectionId}`);
    setStatus(statusConnectingToRelay);
    relayWebsocket.onopen = (event) => {
        setStatus(statusWaitingForStreamingDevice);
        relayConnected = true;
        if (helloMessage != undefined) {
            relayWebsocket.send(helloMessage);
        }
    };
    relayWebsocket.onclose = (event) => {
        if (event.code == closeCodeReUsedConnectionId) {
            setStatus(statusKickedOut);
        } else {
            reset(100);
        }
    };
    relayWebsocket.onmessage = async (event) => {
        setStatus(statusStreamingDeviceConnected);
        if (obsWebsocket != undefined) {
            obsWebsocket.send(event.data);
        }
    };
}

function setupObsWebsocket() {
    obsWebsocket = new WebSocket(`ws://localhost:${obsPort}`);
    setStatus(statusConnectingToObs);
    obsWebsocket.onerror = (event) => {
        setStatus(statusObsError);
    };
    obsWebsocket.onclose = (event) => {
        if (event.code != 1000) {
            reset(5000);
        }
    };
    obsWebsocket.onmessage = async (event) => {
        if (relayConnected) {
            relayWebsocket.send(event.data);
        } else if (JSON.parse(event.data).d.authentication != undefined) {
            helloMessage = event.data;
            setupRelayWebsocket();
        } else {
            setStatus(statusObsAuthDisabledError);
            obsWebsocket.close();
        }
    };
}

function copyMoblinClientUrlToClipboard() {
    navigator.clipboard.writeText(`wss://mys-lang.org/obs-remote-control-relay/client/${connectionId}`);
}

function copyObsBladeHostnameClientUrlToClipboard() {
    navigator.clipboard.writeText(`mys-lang.org/obs-remote-control-relay/client/${connectionId}`);
}

function populateObsPort() {
    document.getElementById('obsPort').value = obsPort;
}

function setObsPort() {
    obsPort = document.getElementById('obsPort').value;
    localStorage.setItem('obsPort', obsPort);
    reset(0);
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

function updateStatus() {
    let statusWithIcon = `<i class="p-icon--spinner u-animation--spin"></i> ${status}`;
    let help = "";
    if (status == statusConnectingToObs) {
        help = `Make sure OBS is running on this computer and that it has the WebSocket Server enabled and configured with port ${obsPort}.`;
    } else if (status == statusConnectingToRelay) {
        help = "Make sure this computer has internet access. Otherwise it cannot connect to the Relay.";
    } else if (status == statusWaitingForStreamingDevice) {
        help = "Configure the OBS remote control in your streaming device to use the Client URL below to make it connect to OBS.";
    } else if (status == statusObsError) {
        help = "Your browser likely does not support insecure websocket connections. Try a different browser. Chrome and Firefox usually works.";
        statusWithIcon = `<i class="p-icon--error"></i> ${status}`;
    } else if (status == statusObsAuthDisabledError) {
        help = "Enable authentication in WebSocket Server settings in OBS.";
        statusWithIcon = `<i class="p-icon--error"></i> ${status}`;
    } else if (status == statusKickedOut) {
        help = "There is likely another tab with the OBS Remote Control Relay open.";
        statusWithIcon = `<i class="p-icon--error"></i> ${status}`;
    } else if (status == statusStreamingDeviceConnected) {
        statusWithIcon = `<i class="p-icon--success"></i> ${status}`;
    }
    document.getElementById('status').innerHTML = statusWithIcon;
    document.getElementById('help').innerHTML = formatHelp(help);
}

function loadConnectionId(urlParams) {
    connectionId = urlParams.get('connectionId');
    if (connectionId == undefined) {
        connectionId = localStorage.getItem('connectionId');
    }
    if (connectionId == undefined) {
        connectionId = crypto.randomUUID();
    }
    localStorage.setItem('connectionId', connectionId);
}

function loadObsPort(urlParams) {
    obsPort = urlParams.get('obsPort');
    if (obsPort == undefined) {
        obsPort = localStorage.getItem('obsPort');
    }
    if (obsPort == undefined) {
        obsPort = "4455";
    }
    localStorage.setItem('obsPort', obsPort);
}

window.addEventListener('DOMContentLoaded', async (event) => {
    const urlParams = new URLSearchParams(window.location.search);
    loadConnectionId(urlParams);
    loadObsPort(urlParams);
    setupObsWebsocket();
    populateObsPort();
    updateStatus();
});
