const r = http.get("https://" + input.host + "/status", { headers: { Authorization: secrets.get("HYPERVISOR_KEY") } })
return JSON.parse(r.body)
