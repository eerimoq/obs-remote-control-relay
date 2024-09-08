const relayStatusConnecting = "Connecting...";
const relayStatusConnected = "Connected";
const relayStatusKicked = "Kicked";

const obsStatusConnecting = "Connecting...";
const obsStatusConnected = "Connected";

const connectionStatusConnectingToRelay = "Connecting to Relay...";
const connectionStatusConnectingToObs = "Connecting to OBS on this computer...";
const connectionStatusObsClosed = "OBS connection closed";
const connectionStatusObsError = "OBS connection error";
const connectionStatusConnected = "Connected";
const connectionStatusRemoteControllerClosed =
  "Remote controller connection closed";
const connectionStatusRemoteControllerError =
  "Remote controller connection error";
const connectionStatusRateLimitExceeded = "Rate limit exceeded";

const defaultObsPort = "4455";

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
    this.bridgeToRemoteControllerBytes = 0;
    this.bridgeToObsBytes = 0;
    this.bitrateToRemoteController = 0;
    this.bitrateToObs = 0;
    this.prevBitrateToRemoteControllerBytes = 0;
    this.prevBitrateToObsBytes = 0;
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
    if (this.isAborted() && newStatus != connectionStatusRateLimitExceeded) {
      return;
    }
    this.status = newStatus;
    this.statusUpdateTime = new Date();
    updateConnections();
  }

  isAborted() {
    return (
      this.status == connectionStatusRemoteControllerClosed ||
      this.status == connectionStatusRemoteControllerError ||
      this.status == connectionStatusObsClosed ||
      this.status == connectionStatusObsError ||
      this.status == connectionStatusRateLimitExceeded
    );
  }

  setupRelayDataWebsocket() {
    this.relayDataWebsocket = new WebSocket(
      `${wsScheme}://${baseUrl}/bridge/data/${bridgeId}/${this.connectionId}`
    );
    this.status = connectionStatusConnectingToRelay;
    this.relayDataWebsocket.onopen = (event) => {
      this.setupObsWebsocket();
    };
    this.relayDataWebsocket.onerror = (event) => {
      this.setStatus(connectionStatusRemoteControllerError);
      this.close();
    };
    this.relayDataWebsocket.onclose = (event) => {
      this.setStatus(connectionStatusRemoteControllerClosed);
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
        this.bridgeToRemoteControllerBytes += textEncoder.encode(
          event.data
        ).length;
        this.relayDataWebsocket.send(event.data);
      }
    };
  }

  updateBitrates() {
    this.bitrateToRemoteController =
      8 *
      (this.bridgeToRemoteControllerBytes -
        this.prevBitrateToRemoteControllerBytes);
    this.prevBitrateToRemoteControllerBytes =
      this.bridgeToRemoteControllerBytes;
    this.bitrateToObs =
      8 * (this.bridgeToObsBytes - this.prevBitrateToObsBytes);
    this.prevBitrateToObsBytes = this.bridgeToObsBytes;
  }
}

class Relay {
  constructor() {
    this.controlWebsocket = undefined;
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
    updateRelayStatus();
  }

  sendStatus(status) {
    if (
      this.controlWebsocket != undefined &&
      this.controlWebsocket.readyState == WebSocket.OPEN
    ) {
      this.controlWebsocket.send(JSON.stringify(status));
    }
  }

  setupControlWebsocket() {
    this.controlWebsocket = new WebSocket(
      `${wsScheme}://${baseUrl}/bridge/control/${bridgeId}`
    );
    this.setStatus(relayStatusConnecting);
    this.controlWebsocket.onopen = (event) => {
      this.setStatus(relayStatusConnected);
    };
    this.controlWebsocket.onerror = (event) => {
      if (this.status != relayStatusKicked) {
        reset(10000);
      }
    };
    this.controlWebsocket.onclose = (event) => {
      if (this.status != relayStatusKicked) {
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
      } else if (message.type == "kicked") {
        this.setStatus(relayStatusKicked);
      } else if (message.type == "rateLimitExceeded") {
        for (const connection of connections) {
          if (connection.connectionId == message.data.connectionId) {
            connection.setStatus(connectionStatusRateLimitExceeded);
          }
        }
      }
    };
  }
}

class Obs {
  constructor() {
    this.websocket = undefined;
    this.status = obsStatusConnecting;
    this.timerId = undefined;
  }

  setStatus(newStatus) {
    if (this.status == newStatus) {
      return;
    }
    this.status = newStatus;
    updateObsStatus();
  }

  setupWebsocket() {
    this.websocket = new WebSocket(`ws://localhost:${obsPort}`);
    this.setStatus(obsStatusConnecting);
    this.websocket.onopen = (event) => {
      this.setStatus(obsStatusConnected);
    };
    this.websocket.onerror = (event) => {
      this.setStatus(obsStatusConnecting);
      this.retry(10000);
    };
    this.websocket.onclose = (event) => {
      this.setStatus(obsStatusConnecting);
      this.retry(10000);
    };
  }

  retry(delayMs) {
    if (this.timerId != undefined) {
      clearTimeout(this.timerId);
    }
    this.timerId = setTimeout(() => {
      this.timerId = undefined;
      this.setupWebsocket();
    }, delayMs);
  }
}

let obs = undefined;
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

function makeMoblinRemoteControllerUrl() {
  return `${wsScheme}://${baseUrl}/remote-controller/${bridgeId}`;
}

function makeObsBladeHostnameRemoteControllerUrl() {
  return `${baseUrl}/remote-controller/${bridgeId}`;
}

function copyMoblinRemoteControllerUrlToClipboard() {
  navigator.clipboard.writeText(makeMoblinRemoteControllerUrl());
}

function copyObsBladeHostnameRemoteControllerUrlToClipboard() {
  navigator.clipboard.writeText(makeObsBladeHostnameRemoteControllerUrl());
}

function makeStatusPageUrl() {
  return `${httpScheme}://${baseUrl}/status.html?bridgeId=${bridgeId}`;
}

function copyStatusPageUrlToClipboard() {
  navigator.clipboard.writeText(makeStatusPageUrl());
}

function toggleShow(inputId, iconId) {
  let input = document.getElementById(inputId);
  let icon = document.getElementById(iconId);
  if (input.type === "password") {
    input.type = "text";
    icon.classList.add("p-icon--hide");
    icon.classList.remove("p-icon--show");
  } else {
    input.type = "password";
    icon.classList.add("p-icon--show");
    icon.classList.remove("p-icon--hide");
  }
}

function toggleShowMoblinRemoteControllerMoblinUrl() {
  toggleShow("moblinUrl", "moblinUrlIcon");
}

function toggleShowMoblinRemoteControllerObsBladeHostname() {
  toggleShow("obsBladeHostname", "obsBladeHostnameIcon");
}

function toggleShowMoblinRemoteControllerObsBladeHost() {
  toggleShow("obsBladeHost", "obsBladeHostIcon");
}

function toggleShowStatusPageUrl() {
  toggleShow("statusPageUrl", "statusPageUrlIcon");
}

function populateRemoteControllerSetup() {
  document.getElementById("moblinUrl").value = makeMoblinRemoteControllerUrl();
  document.getElementById("obsBladeHostname").value =
    makeObsBladeHostnameRemoteControllerUrl();
  document.getElementById("obsBladeHost").value =
    makeMoblinRemoteControllerUrl();
}

function populateSettings() {
  document.getElementById("obsPort").value = obsPort;
  document.getElementById("bridgeId").value = bridgeId;
}

function populateStatusPage() {
  document.getElementById("statusPageUrl").value = makeStatusPageUrl();
}

function saveSettings() {
  obsPort = document.getElementById("obsPort").value;
  localStorage.setItem("obsPort", obsPort);
  bridgeId = document.getElementById("bridgeId").value;
  localStorage.setItem("bridgeId", bridgeId);
  populateRemoteControllerSetup();
  populateStatusPage();
  reset(0);
  obs.retry(0);
}

function resetSettings() {
  bridgeId = crypto.randomUUID();
  localStorage.setItem("bridgeId", bridgeId);
  obsPort = defaultObsPort;
  localStorage.setItem("obsPort", obsPort);
  populateRemoteControllerSetup();
  populateSettings();
  populateStatusPage();
  reset(0);
  obs.retry(0);
}

function updateConnections() {
  let body = getTableBody("connections");
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
    connections: [],
  };
  for (const connection of connections) {
    status.connections.push({
      status: connection.status,
      aborted: connection.isAborted(),
      statusUpdateTime: connection.statusUpdateTime,
      bitrateToRemoteController: connection.bitrateToRemoteController,
      bitrateToObs: connection.bitrateToObs,
    });
  }
  relay.sendStatus(status);
}

function updateRelayStatus() {
  let relayStatus = '<i class="p-icon--error"></i> Unknown server status';
  if (relay.status == relayStatusConnecting) {
    relayStatus =
      '<i class="p-icon--spinner u-animation--spin"></i> Connecting to server';
  } else if (relay.status == relayStatusConnected) {
    relayStatus = '<i class="p-icon--success"></i> Connected to server';
  } else if (relay.status == relayStatusKicked) {
    relayStatus = '<i class="p-icon--error"></i> Kicked by server';
  }
  document.getElementById("relayStatus").innerHTML = relayStatus;
}

function updateObsStatus() {
  let obsStatus = '<i class="p-icon--error"></i> Unknown OBS status';
  if (obs.status == obsStatusConnecting) {
    obsStatus =
      '<i class="p-icon--spinner u-animation--spin"></i> Connecting to OBS on this computer (may take up to a minute)';
  } else if (obs.status == obsStatusConnected) {
    obsStatus =
      '<i class="p-icon--success"></i> Connected to OBS on this computer';
  }
  document.getElementById("obsStatus").innerHTML = obsStatus;
}

function toggleShowBridgeId() {
  let bridgeIdInput = document.getElementById("bridgeId");
  let bridgeIdText = document.getElementById("bridgeIdText");
  let bridgeIdIcon = document.getElementById("bridgeIdIcon");
  if (bridgeIdInput.type === "password") {
    bridgeIdInput.type = "text";
    bridgeIdText.innerText = "Hide";
    bridgeIdIcon.classList.add("p-icon--hide");
    bridgeIdIcon.classList.remove("p-icon--show");
  } else {
    bridgeIdInput.type = "password";
    bridgeIdText.innerText = "Show";
    bridgeIdIcon.classList.add("p-icon--show");
    bridgeIdIcon.classList.remove("p-icon--hide");
  }
}

function loadbridgeId(urlParams) {
  bridgeId = urlParams.get("bridgeId");
  if (bridgeId == undefined) {
    bridgeId = localStorage.getItem("bridgeId");
  }
  if (bridgeId == undefined) {
    bridgeId = crypto.randomUUID();
  }
  localStorage.setItem("bridgeId", bridgeId);
}

function loadObsPort(urlParams) {
  obsPort = urlParams.get("obsPort");
  if (obsPort == undefined) {
    obsPort = localStorage.getItem("obsPort");
  }
  if (obsPort == undefined) {
    obsPort = defaultObsPort;
  }
  localStorage.setItem("obsPort", obsPort);
}

window.addEventListener("DOMContentLoaded", async (event) => {
  const urlParams = new URLSearchParams(window.location.search);
  loadbridgeId(urlParams);
  loadObsPort(urlParams);
  relay = new Relay();
  relay.setupControlWebsocket();
  obs = new Obs();
  obs.setupWebsocket();
  populateRemoteControllerSetup();
  populateSettings();
  populateStatusPage();
  updateConnections();
  updateRelayStatus();
  updateObsStatus();
  setInterval(() => {
    for (const connection of connections) {
      connection.updateBitrates();
    }
    updateConnections();
    updateStatus();
  }, 1000);
});
