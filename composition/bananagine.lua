-- Bananagine application workflow: registry slice.
--
-- The registry engine owns state. This Lua file owns the application-level
-- sequencing and can be reused by HTTP or sibling-call façades.

local registry_target = "bananagine-registry"

-- Lifecycle event routing is intentionally deferred until the application has
-- a validated event sink; emitting here would otherwise discard the events.
local function registry_call(operation, request)
  return pulp.call(registry_target, operation, request or {})
end

pulp.on("bananagine.registry.v1.register", function(server)
  return registry_call(
    "bananagine.registry.v1.register",
    { server = server }
  )
end)

pulp.on("bananagine.registry.v1.list", function(filter)
  return registry_call("bananagine.registry.v1.list", filter or {})
end)

pulp.on("bananagine.registry.v1.get", function(request)
  return registry_call("bananagine.registry.v1.get", request)
end)

pulp.on("bananagine.registry.v1.update", function(request)
  return registry_call("bananagine.registry.v1.update", request)
end)

pulp.on("bananagine.registry.v1.unregister", function(request)
  return registry_call("bananagine.registry.v1.unregister", request)
end)

pulp.on("bananagine.registry.v1.set_players", function(request)
  return registry_call("bananagine.registry.v1.set_players", request)
end)

pulp.on("bananagine.registry.v1.put_match", function(request)
  return registry_call("bananagine.registry.v1.put_match", request)
end)

pulp.on("bananagine.registry.v1.remove_match", function(request)
  return registry_call("bananagine.registry.v1.remove_match", request)
end)
