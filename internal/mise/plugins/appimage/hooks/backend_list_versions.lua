function PLUGIN:BackendListVersions(ctx)
  local http = require("http")
  local json = require("json")

  -- http.get throws in mise and returns (resp, err) in vfox; pcall makes the
  -- call non-throwing in both, and the HTTP status is checked explicitly so a
  -- rate-limit or 404 body is never decoded as version data.
  local ok, resp, err = pcall(http.get, {
    url = "https://api.github.com/repos/" .. ctx.tool .. "/releases?per_page=100",
  })
  if not ok or not resp then
    error("failed to fetch releases: " .. tostring(err or resp))
  end
  if resp.status_code ~= 200 then
    error("failed to fetch releases: HTTP " .. resp.status_code)
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
