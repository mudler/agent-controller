// Logins page. The token stays in a variable for this page only; it is
// never written to storage or cookies. Values are sent, never read back.
"use strict";

let token = "";

const $ = (id) => document.getElementById(id);

function setStatus(text) {
  $("status").textContent = text;
}

async function api(method, path, body) {
  const opts = { method, headers: { Authorization: "Bearer " + token } };
  if (body !== undefined) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  const resp = await fetch(path, opts);
  const data = await resp.json().catch(() => ({}));
  if (!resp.ok) {
    const e = data.error || {};
    throw new Error((e.field ? e.field + ": " : "") + (e.reason || resp.statusText));
  }
  return data;
}

function cell(text) {
  const td = document.createElement("td");
  td.textContent = text == null ? "" : String(text);
  return td;
}

async function load() {
  token = $("token").value.trim();
  setStatus("Loading...");
  try {
    const logins = await api("GET", "/v1/logins");
    const body = $("logins");
    body.replaceChildren();
    for (const l of logins) {
      const tr = document.createElement("tr");
      tr.append(cell(l.name), cell(l.kind), cell((l.repos || []).join(", ")), cell(l.egressAll ? "yes" : "no"),
        cell(l.description), cell(l.rotatedAt), cell(l.rotatedBy));
      const td = document.createElement("td");
      const del = document.createElement("button");
      del.type = "button";
      del.textContent = "Delete";
      del.addEventListener("click", () => remove(l.name));
      td.append(del);
      tr.append(td);
      body.append(tr);
    }
    setStatus(logins.length + " logins");
  } catch (err) {
    setStatus("Error: " + err.message);
  }
}

async function remove(name) {
  if (!window.confirm("Delete login " + name + " and its stored value?")) {
    return;
  }
  try {
    await api("DELETE", "/v1/logins/" + encodeURIComponent(name));
    await load();
  } catch (err) {
    setStatus("Error: " + err.message);
  }
}

function readFileBase64(file) {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result).split(",", 2)[1] || "");
    reader.onerror = () => reject(reader.error);
    reader.readAsDataURL(file);
  });
}

async function save(event) {
  event.preventDefault();
  const kind = $("kind").value;
  const body = { kind, description: $("description").value };
  if (kind === "github-token") {
    body.repos = $("repos").value.split(",").map((s) => s.trim()).filter(Boolean);
    body.egressAll = $("egressAll").checked;
  }
  const file = $("file").files[0];
  if (file) {
    body.valueBase64 = await readFileBase64(file);
  } else if ($("value").value !== "") {
    body.value = $("value").value;
  }
  try {
    await api("PUT", "/v1/logins/" + encodeURIComponent($("name").value), body);
    $("value").value = "";
    $("file").value = "";
    await load();
    setStatus("Saved " + $("name").value);
  } catch (err) {
    setStatus("Error: " + err.message);
  }
}

document.addEventListener("DOMContentLoaded", () => {
  $("load").addEventListener("click", load);
  $("form").addEventListener("submit", save);
});
