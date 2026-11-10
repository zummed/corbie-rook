if (!/^[a-z0-9-]+$/.test(input.name)) throw new Error("not a machine's name: " + input.name)
const r = http.post("https://" + input.host + "/restart", { headers: { Authorization: secrets.get("HYPERVISOR_KEY") }, body: input.name })
return r.body
