-- Bananagine application workflow: registry slice.
--
-- The registry engine owns state. This Lua file owns the application-level
-- sequencing and can be reused by HTTP or sibling-call façades.

-- One physical Pulp-native state artifact retains both logical provider
-- surfaces. The names used below remain unchanged at the capability level.
local registry_target = "runtime-directory"
local template_catalog_target = "template-catalog"
local worker_target = "async-http-job"
local contracts = "contracts.v1"

local function opaque(value)
  return { version = contracts, value = value }
end

local function workload(value)
  return { version = contracts, id = opaque(value) }
end

local function node(value)
  return { version = contracts, id = opaque(value) }
end

local function quantity(cpu, memory, storage)
  return {
    version = contracts,
    cpu_millicores = cpu,
    memory_bytes = memory,
    storage_bytes = storage,
  }
end

local function key(value)
  return { version = contracts, value = value }
end

-- Lifecycle event routing is intentionally deferred until the application has
-- a validated event sink; emitting here would otherwise discard the events.
local function registry_call(operation, request)
  return pulp.call(registry_target, operation, request or {})
end

local function template_catalog_call(operation, request)
  return pulp.call(template_catalog_target, operation, request or {})
end

local function worker_call(operation, request)
  return pulp.call(worker_target, operation, request or {})
end

pulp.on("runtime-directory.v1.register", function(server)
  return registry_call(
    "runtime-directory.v1.register",
    { server = server }
  )
end)

pulp.on("runtime-directory.v1.list", function(filter)
  return registry_call("runtime-directory.v1.list", filter or {})
end)

pulp.on("runtime-directory.v1.get", function(request)
  return registry_call("runtime-directory.v1.get", request)
end)

pulp.on("runtime-directory.v1.update", function(request)
  return registry_call("runtime-directory.v1.update", request)
end)

pulp.on("runtime-directory.v1.unregister", function(request)
  return registry_call("runtime-directory.v1.unregister", request)
end)

pulp.on("runtime-directory.v1.set_players", function(request)
  return registry_call("runtime-directory.v1.set_players", request)
end)

pulp.on("runtime-directory.v1.put_match", function(request)
  return registry_call("runtime-directory.v1.put_match", request)
end)

pulp.on("runtime-directory.v1.remove_match", function(request)
  return registry_call("runtime-directory.v1.remove_match", request)
end)

pulp.on("template-catalog.v1.replace", function(request)
  return template_catalog_call("template-catalog.v1.replace", request)
end)

pulp.on("template-catalog.v1.list", function(request)
  return template_catalog_call("template-catalog.v1.list", request)
end)

pulp.on("template-catalog.v1.get", function(request)
  return template_catalog_call("template-catalog.v1.get", request)
end)

pulp.on("template-catalog.v1.snapshot.export", function(request)
  return template_catalog_call("template-catalog.v1.snapshot.export", request)
end)

pulp.on("template-catalog.v1.snapshot.import", function(request)
  return template_catalog_call("template-catalog.v1.snapshot.import", request)
end)

pulp.on("async-http-job.v1.http.submit", function(request)
  return worker_call("async-http-job.v1.http.submit", request)
end)

pulp.on("async-http-job.v1.status", function(request)
  return worker_call("async-http-job.v1.status", request)
end)

pulp.on("async-http-job.v1.cancel", function(request)
  return worker_call("async-http-job.v1.cancel", request)
end)

pulp.on("async-http-job.v1.snapshot.export", function(request)
  return worker_call("async-http-job.v1.snapshot.export", request)
end)

pulp.on("async-http-job.v1.snapshot.import", function(request)
  return worker_call("async-http-job.v1.snapshot.import", request)
end)

-- Translate Bananagine's named server/template intent into the opaque,
-- reusable workload contracts. The privileged Docker effect remains in the
-- compatibility facade until effect settlement has production parity.
pulp.on("bananagine.create-plan.v1", function(request)
  local server_id = assert(request.server_id, "server_id is required")
  local template_name = assert(request.template, "template is required")
  local node_id = assert(request.node_id, "node_id is required")
  local issued_at = assert(request.issued_at, "issued_at is required")
  local catalog = template_catalog_call("template-catalog.v1.get", { name = template_name })
  if not catalog.ok then
    error("bananagine template lookup failed")
  end

  local entry = catalog.value
  local selected_workload = workload("bananagine/server/" .. server_id)
  local selected_node = node(node_id)
  local requested = quantity(math.floor(entry.cpu_limit * 1000), entry.memory_limit, request.storage_bytes)
  local prefix = "bananagine/create/" .. server_id
  local reservation_id = opaque(prefix .. "/reservation")

  local created = pulp.call("workload-inventory", "workload-inventory.v1.workload.create", {
    version = "workload-inventory.v1", command_id = key(prefix .. "/workload"),
    workload = selected_workload, node = selected_node, lifecycle = "requested",
  })
  local capacity = pulp.call("capacity-scheduler", "capacity-scheduler.v1.capacity.set", {
    version = "capacity-scheduler.v1", command_id = prefix .. "/capacity", node = selected_node,
    capacity = quantity(request.node_cpu_millicores, request.node_memory_bytes, request.node_storage_bytes),
    expected_generation = 0,
  })
  local reserved = pulp.call("capacity-scheduler", "capacity-scheduler.v1.reserve", {
    version = "capacity-scheduler.v1", command_id = prefix .. "/reserve", reservation_id = reservation_id,
    workload = selected_workload, node = selected_node, resources = requested, lease_seconds = 300,
    eligibility_evidence = { version = "capacity-scheduler.v1", value = "bananagine-template-admitted" },
    idempotency = key(prefix .. "/reserve"),
  })
  local provisioned = pulp.call("workload-provisioning", "workload-provisioning.v1.provision", {
    Envelope = { Version = "workload-provisioning.v1", CommandID = key(prefix .. "/provision"), Idempotency = key(prefix .. "/provision") },
    Command = { version = contracts, id = key(prefix .. "/provision"), workload = selected_workload, node = selected_node,
      reservation = reservation_id, expected_node_generation = 1, issued_at = issued_at, idempotency = key(prefix .. "/provision") },
    Template = opaque("bananagine/template/" .. entry.name),
    ResourceProfile = opaque("bananagine/template/" .. entry.name .. "/resources"), Resources = requested,
  })
  local runtime = pulp.call("runtime-control", "runtime-control.v1.desired.apply", {
    version = "runtime-control.v1", id = key(prefix .. "/runtime"), workload = selected_workload, node = selected_node,
    desired = "running", expected_generation = 0, idempotency = key(prefix .. "/runtime"), requested_at = issued_at,
  })
  return { template = entry, workload = created.workload, capacity = capacity.inventory,
    reservation = reserved.reservation, provision = provisioned.Workload, runtime = runtime }
end)
