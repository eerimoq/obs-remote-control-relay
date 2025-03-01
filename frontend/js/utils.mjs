const secure = `${window.location.protocol == "https:" ? "s" : ""}`;
export const wsScheme = `ws${secure}`;
export const httpScheme = `http${secure}`;

function numberSuffix(value) {
  return value == 1 ? "" : "s";
}

export function timeAgoString(fromDate) {
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

export function bitrateToString(bitrate) {
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

export function bytesToString(bytes) {
  if (bytes < 1000) {
    return `${bytes} B`;
  } else if (bytes < 1000000) {
    let bytesKb = (bytes / 1000).toFixed(1);
    return `${bytesKb} kB`;
  } else if (bytes < 1000000000) {
    let bytesMb = (bytes / 1000000).toFixed(1);
    return `${bytesMb} MB`;
  } else {
    let bytesGb = (bytes / 1000000000).toFixed(1);
    return `${bytesGb} GB`;
  }
}

export function getTableBody(id) {
  let table = document.getElementById(id);
  while (table.rows.length > 1) {
    table.deleteRow(-1);
  }
  return table.tBodies[0];
}

export function appendToRow(row, value) {
  let cell = row.insertCell(-1);
  cell.innerHTML = value;
}

export function addOnClick(elementId, func) {
  document.getElementById(elementId).addEventListener("click", func);
}
