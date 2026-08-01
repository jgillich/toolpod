function PLUGIN:BackendListVersions(ctx)
  local http = require("http")
  local json = require("json")

  local resp, err = http.get({
    url = "https://api.github.com/repos/" .. ctx.tool .. "/releases?per_page=100",
  })
  if err ~= nil then
    error("failed to fetch releases: " .. err)
  end

  local releases = json.decode(resp.body)
  local versions = {}
  if type(releases) == "table" then
    for _, r in ipairs(releases) do
      if not r.prerelease then
        table.insert(versions, (r.tag_name:gsub("^v", "")))
      end
    end
  end
  return { versions = versions }
end
