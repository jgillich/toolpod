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
        local ver = r.tag_name:match("v(%d[%w%._+-]*)$")
        if ver then
          table.insert(versions, ver)
        end
      end
    end
    table.sort(versions, function(a, b)
      local function parts(v)
        local t = {}
        for num in v:gmatch("%d+") do
          table.insert(t, tonumber(num))
        end
        return t
      end
      local pa, pb = parts(a), parts(b)
      for i = 1, math.max(#pa, #pb) do
        local x, y = pa[i] or 0, pb[i] or 0
        if x ~= y then
          return x < y
        end
      end
      return false
    end)
  end
  return { versions = versions }
end
