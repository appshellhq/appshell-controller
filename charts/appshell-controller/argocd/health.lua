-- Argo CD health for AppshellApplication (registry.appshell.org), from the controller's status.
--
-- Install it as resource.customizations.health.registry.appshell.org_AppshellApplication in
-- argocd-cm. It reads only what the controller reports, following the Kubernetes conventions:
--
--   no status yet, or a status for an older generation  Progressing
--   Ready=True                                           Healthy
--   Ready=False                                          Degraded, with the controller's reason
--   Ready=Unknown, or no Ready condition                 Progressing
local hs = { status = "Progressing", message = "Waiting for the controller to apply this generation" }

if obj.status == nil or obj.status.observedGeneration == nil then
  return hs
end
if obj.metadata.generation ~= nil and obj.status.observedGeneration < obj.metadata.generation then
  return hs
end

if obj.status.conditions ~= nil then
  for _, condition in ipairs(obj.status.conditions) do
    if condition.type == "Ready" then
      if condition.status == "True" then
        hs.status = "Healthy"
        hs.message = condition.message
      elseif condition.status == "False" then
        hs.status = "Degraded"
        hs.message = condition.reason .. ": " .. (condition.message or "")
      else
        hs.message = condition.message
      end
      return hs
    end
  end
end

return hs
