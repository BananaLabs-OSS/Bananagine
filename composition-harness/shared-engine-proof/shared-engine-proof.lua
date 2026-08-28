-- Isolated non-production Bananagine application workflow.
--
-- This deliberately owns no game policy or durable state. It is the narrow
-- instruction-set seam through which a later proof drives the unchanged
-- generic workload engines.

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

local function template_entry(name)
  local result = pulp.call("template-catalog", "template-catalog.v1.get", { name = name })
  if not result.ok then
    error("bananagine template lookup failed")
  end
  return result.value
end

pulp.on("health", function()
  return { ok = true, application = "bananagine-shared-engine-proof" }
end)

-- The proof is intentionally application logic, not engine policy: it gives
-- opaque identities and resource intent to unchanged generic operators.
pulp.on("bananagine.shared-engine-proof.v1.run", function()
  local selected_workload = workload("bananagine-proof-workload")
  local selected_node = node("bananagine-proof-node")
  local requested = quantity(1000, 1073741824, 1073741824)
  local issued_at = "2026-08-09T12:00:00Z"

  local created = pulp.call("workload-inventory", "workload-inventory.v1.workload.create", {
    version = "workload-inventory.v1",
    command_id = key("bananagine-proof-workload-create"),
    workload = selected_workload,
    node = selected_node,
    lifecycle = "requested",
  })

  local capacity = pulp.call("capacity-scheduler", "capacity-scheduler.v1.capacity.set", {
    version = "capacity-scheduler.v1",
    command_id = "bananagine-proof-capacity-set",
    node = selected_node,
    capacity = quantity(4000, 4294967296, 4294967296),
    expected_generation = 0,
  })

  local reserved = pulp.call("capacity-scheduler", "capacity-scheduler.v1.reserve", {
    version = "capacity-scheduler.v1",
    command_id = "bananagine-proof-reserve",
    reservation_id = opaque("bananagine-proof-reservation"),
    workload = selected_workload,
    node = selected_node,
    resources = requested,
    lease_seconds = 300,
    eligibility_evidence = { version = "capacity-scheduler.v1", value = "admitted-by-bananagine-proof" },
    idempotency = key("bananagine-proof-reserve"),
  })

  local provisioned = pulp.call("workload-provisioning", "workload-provisioning.v1.provision", {
    Envelope = {
      Version = "workload-provisioning.v1",
      CommandID = opaque("bananagine-proof-provision"),
      Idempotency = key("bananagine-proof-provision"),
    },
    Command = {
      version = contracts,
      id = opaque("bananagine-proof-provision"),
      workload = selected_workload,
      node = selected_node,
      reservation = opaque("bananagine-proof-reservation"),
      expected_node_generation = 1,
      issued_at = issued_at,
      idempotency = key("bananagine-proof-provision"),
    },
    Template = opaque("bananagine-proof-template"),
    ResourceProfile = opaque("bananagine-proof-profile"),
    Resources = requested,
  })

  local runtime = pulp.call("runtime-control", "runtime-control.v1.desired.apply", {
    version = "runtime-control.v1",
    id = opaque("bananagine-proof-runtime"),
    workload = selected_workload,
    node = selected_node,
    desired = "running",
    expected_generation = 0,
    idempotency = key("bananagine-proof-runtime"),
    requested_at = issued_at,
  })

  return {
    workload = created.workload,
    capacity = capacity.inventory,
    reservation = reserved.reservation,
    provision = provisioned.Workload,
    runtime = runtime,
  }
end)

-- This is deliberately Bananagine application logic. It maps a named
-- Bananagine template and server identity to opaque generic-engine intent;
-- it does not inspect engine internals or perform Docker work. A later
-- Bananagine-only privileged adapter will settle the durable provision effect.
pulp.on("bananagine.create-plan.v1.run", function(request)
  local server_id = assert(request.server_id, "server_id is required")
  local template_name = assert(request.template, "template is required")
  local node_id = assert(request.node_id, "node_id is required")
  local issued_at = assert(request.issued_at, "issued_at is required")
  local entry = template_entry(template_name)
  local selected_workload = workload("bananagine/server/" .. server_id)
  local selected_node = node(node_id)
  local requested = quantity(math.floor(entry.cpu_limit * 1000), entry.memory_limit, request.storage_bytes)
  local prefix = "bananagine/create/" .. server_id

  local created = pulp.call("workload-inventory", "workload-inventory.v1.workload.create", {
    version = "workload-inventory.v1",
    command_id = key(prefix .. "/workload"),
    workload = selected_workload,
    node = selected_node,
    lifecycle = "requested",
  })
  local capacity = pulp.call("capacity-scheduler", "capacity-scheduler.v1.capacity.set", {
    version = "capacity-scheduler.v1",
    command_id = prefix .. "/capacity",
    node = selected_node,
    capacity = quantity(request.node_cpu_millicores, request.node_memory_bytes, request.node_storage_bytes),
    expected_generation = 0,
  })
  local reservation_id = opaque(prefix .. "/reservation")
  local reserved = pulp.call("capacity-scheduler", "capacity-scheduler.v1.reserve", {
    version = "capacity-scheduler.v1",
    command_id = prefix .. "/reserve",
    reservation_id = reservation_id,
    workload = selected_workload,
    node = selected_node,
    resources = requested,
    lease_seconds = 300,
    eligibility_evidence = { version = "capacity-scheduler.v1", value = "bananagine-template-admitted" },
    idempotency = key(prefix .. "/reserve"),
  })
  local provisioned = pulp.call("workload-provisioning", "workload-provisioning.v1.provision", {
    Envelope = { Version = "workload-provisioning.v1", CommandID = key(prefix .. "/provision"), Idempotency = key(prefix .. "/provision") },
    Command = { version = contracts, id = key(prefix .. "/provision"), workload = selected_workload, node = selected_node, reservation = reservation_id, expected_node_generation = 1, issued_at = issued_at, idempotency = key(prefix .. "/provision") },
    Template = opaque("bananagine/template/" .. entry.name),
    ResourceProfile = opaque("bananagine/template/" .. entry.name .. "/resources"),
    Resources = requested,
  })
  local runtime = pulp.call("runtime-control", "runtime-control.v1.desired.apply", {
    version = "runtime-control.v1", id = key(prefix .. "/runtime"), workload = selected_workload, node = selected_node,
    desired = "running", expected_generation = 0, idempotency = key(prefix .. "/runtime"), requested_at = issued_at,
  })
  return { template = entry, workload = created.workload, capacity = capacity.inventory, reservation = reserved.reservation, provision = provisioned.Workload, runtime = runtime }
end)
