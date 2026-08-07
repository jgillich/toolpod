local function shq(s)
  return "'" .. s:gsub("'", "'\\''") .. "'"
end

local function has_control(s)
  for i = 1, #s do
    local b = s:byte(i)
    if b < 0x20 or b == 0x7f then
      return true
    end
  end
  return false
end

local function safe_relative_path(s, allow_nested)
  if type(s) ~= "string" or s == "" or has_control(s) then
    return false
  end
  if s:sub(1, 1) == "/" or s:sub(-1) == "/" or s:find("\\", 1, true) then
    return false
  end
  if not allow_nested and s:find("/", 1, true) then
    return false
  end
  if not s:match("^[A-Za-z0-9._+%-/]+$") then
    return false
  end
  for part in s:gmatch("[^/]+") do
    if part == "." or part == ".." then
      return false
    end
  end
  return true
end

-- GET a URL and return the body string. http.get throws in mise and returns
-- (resp, err) in vfox, so it is pcall-wrapped for portability; both runtimes
-- report HTTP status separately, and non-200 bodies (e.g. GitHub rate-limit
-- pages) must not be treated as data.
local function http_get_body(url)
  local ok, resp, err = pcall(http.get, { url = url })
  if not ok or not resp then
    return nil, "request error: " .. tostring(err or resp)
  end
  if resp.status_code ~= 200 then
    return nil, "HTTP " .. resp.status_code
  end
  return resp.body
end

function PLUGIN:BackendInstall(ctx)
  local cmd = require("cmd")
  local json = require("json")
  local file = require("file")
  local http = require("http")

  if RUNTIME.osType ~= "linux" then
    error("appimage: backend requires Linux")
  end

  local repo = ctx.tool
  local version = ctx.version
  local install_path = ctx.install_path
  local download_path = ctx.download_path
  local options = ctx.options

  -- Validate before any network or filesystem mutation; the launcher embeds
  -- these verbatim, so a hostile exe/name must never reach a path on disk.
  local exe = options.exe or "AppRun"
  local name = options.name or repo:match("([^/]+)$")
  if not safe_relative_path(exe, true) or not safe_relative_path(name, false) then
    error("appimage: invalid exe/name option for " .. repo)
  end

  local function find_release(repo, version)
    local url
    if version == "latest" then
      url = "https://api.github.com/repos/" .. repo .. "/releases/latest"
    else
      url = "https://api.github.com/repos/" .. repo .. "/releases/tags/v" .. version
    end
    local body = http_get_body(url)
    if body then
      local release = json.decode(body)
      if type(release) == "table" and release.assets then
        return release
      end
    end
    -- fallback for pinned versions: list releases and match v(%d[%w%._+-]*)$
    -- ("latest" intentionally never matches here, so a bare "latest" that
    -- reaches the fallback still fails closed)
    local list, lerr = http_get_body("https://api.github.com/repos/" .. repo .. "/releases?per_page=100")
    if lerr ~= nil then
      error("failed to fetch releases: " .. lerr)
    end
    local releases = json.decode(list or "")
    if type(releases) == "table" then
      for _, r in ipairs(releases) do
        if not r.prerelease then
          local ver = r.tag_name:match("v(%d[%w%._+-]*)$")
          if ver == version then
            for _, a in ipairs(r.assets or {}) do
              if a.name:match("%.AppImage$") then
                return r
              end
            end
          end
        end
      end
    end
    error("no release found for " .. repo .. "@" .. version)
  end

  local release = find_release(repo, version)

  if options.asset_pattern then
    -- Lua patterns have no backtracking blowup, but a pathological pattern
    -- can still waste CPU; bound its size to keep install time sane.
    if #options.asset_pattern > 200 or has_control(options.asset_pattern) then
      error("appimage: invalid asset_pattern for " .. repo)
    end
  end

  local asset
  if options.asset_pattern then
    for _, a in ipairs(release.assets) do
      if a.name:match("%.AppImage$") and a.name:match(options.asset_pattern) then
        asset = a
        break
      end
    end
  else
    -- prefer an AppImage matching the current arch, else any AppImage
    local arch_names = RUNTIME.archType == "amd64" and { "x86_64", "x64", "amd64" } or { "aarch64", "arm64" }
    for _, a in ipairs(release.assets) do
      if a.name:match("%.AppImage$") then
        local lower = a.name:lower()
        for _, n in ipairs(arch_names) do
          if lower:find(n) then
            asset = a
            break
          end
        end
      end
      if asset then
        break
      end
    end
    if not asset then
      for _, a in ipairs(release.assets) do
        if a.name:match("%.AppImage$") then
          asset = a
          break
        end
      end
    end
  end
  if not asset then
    error("no AppImage asset found for " .. repo .. "@" .. version)
  end

  -- Expected digest: explicit author pin, then GitHub's published per-asset
  -- digest, then a checksum sidecar. Fails closed when none exists, because
  -- an unverified download is what this plugin exists to prevent.
  local function sidecar_digest(assets, asset_name)
    for _, a in ipairs(assets or {}) do
      local is_summary = a.name:match("SHA256SUMS$")
      local is_single = a.name == asset_name .. ".sha256"
      if is_summary or is_single then
        local body = http_get_body(a.browser_download_url)
        if body then
          for line in body:gmatch("[^\r\n]+") do
            local sum, fname = line:match("^(%x+)%s+[%*]?(.+)%s*$")
            if sum and (fname == asset_name or fname:match("[^/]*$") == asset_name) then
              return sum
            end
          end
        end
      end
    end
    return nil
  end

  local sha256 = options.sha256
  local expected = sha256
  if type(sha256) == "table" then
    expected = sha256[RUNTIME.archType]
    -- mise reports aarch64 hosts as "arm64", so also honor the schema's
    -- "aarch64" key (schema validation forbids "arm64" as an arch key).
    if not expected and RUNTIME.archType == "arm64" then
      expected = sha256["aarch64"]
    end
  end
  if not expected or expected == "" then
    expected = asset.digest
    if expected then
      expected = expected:gsub("^sha256:", "")
    end
  end
  if not expected or expected == "" then
    expected = sidecar_digest(release.assets, asset.name)
  end
  if not expected or expected == "" then
    error("appimage: no published digest for " .. repo .. "@" .. version ..
          "; set an explicit sha256 in the tool config")
  end

  local appimage = file.join_path(download_path, "app.AppImage")
  -- http.download_file throws in mise (including on non-2xx via error_for_status)
  -- but returns a single error value in vfox; pcall normalizes both.
  local dload_ok, dload_err = pcall(http.download_file, { url = asset.browser_download_url }, appimage)
  if not dload_ok or dload_err ~= nil then
    error("download failed: " .. tostring(dload_err))
  end

  local actual = cmd.exec("sha256sum " .. shq(appimage)):match("^(%x+)")
  if not actual or actual:lower() ~= expected:lower() then
    error("appimage: sha256 mismatch for " .. repo .. ": got " .. tostring(actual) ..
          ", want " .. expected)
  end

  cmd.exec("chmod +x " .. shq(appimage))

  cmd.exec("cd " .. shq(download_path) .. " && ./app.AppImage --appimage-extract >/dev/null")

  cmd.exec("mkdir -p " .. shq(file.join_path(install_path, "app")))
  cmd.exec("cp -a " .. shq(download_path .. "/squashfs-root/.") .. " " .. shq(file.join_path(install_path, "app")) .. "/")

  -- Swap the bundled xdg-open for the image wrapper that forwards URLs to the
  -- host's XDG desktop portal (AppRun prepends the bundle's usr/bin to PATH).
  -- The wrapper is staged to a temp name first and the original renamed aside
  -- only once the swap can complete; on any failure the original is restored,
  -- and the trailing `|| true` keeps cmd.exec from raising on a restore miss.
  local xdg = file.join_path(install_path, "app", "usr", "bin", "xdg-open")
  local xdg_new = xdg .. ".new"
  local xdg_real = xdg .. ".real"
  cmd.exec("if [ -f " .. shq(xdg) .. " ] && [ -f /usr/local/bin/xdg-open ]; then cp /usr/local/bin/xdg-open " .. shq(xdg_new) .. " && mv " .. shq(xdg) .. " " .. shq(xdg_real) .. " && mv " .. shq(xdg_new) .. " " .. shq(xdg) .. " && chmod +x " .. shq(xdg) .. " || { [ -f " .. shq(xdg_real) .. " ] && mv " .. shq(xdg_real) .. " " .. shq(xdg) .. " || true; }; fi")

  local launcher = '#!/usr/bin/env bash\nexec "$(dirname "$0")/../app/' .. exe .. '" "$@"\n'
  local bin_dir = file.join_path(install_path, "bin")
  local launcher_path = file.join_path(bin_dir, name)
  cmd.exec("mkdir -p " .. shq(bin_dir))
  cmd.exec("cat > " .. shq(launcher_path) .. " <<'APPIMAGE_EOF'\n" .. launcher .. "APPIMAGE_EOF")
  cmd.exec("chmod +x " .. shq(launcher_path))

  -- Record what resolved: latest maps to a concrete release, but the backend
  -- re-runs only when the version dir is absent, so this file is the audit
  -- trail of the tag/asset/digest a machine actually pinned itself to.
  local state = json.encode({ repo = repo, version = release.tag_name:gsub("^v", ""),
                              asset = asset.name, digest = expected })
  local state_path = file.join_path(install_path, ".tpd-resolved")
  cmd.exec("cat > " .. shq(state_path) .. " <<'TPD_RESOLVED_EOF'\n" .. state .. "\nTPD_RESOLVED_EOF")

  return {}
end
