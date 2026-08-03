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

function PLUGIN:BackendInstall(ctx)
  local cmd = require("cmd")
  local json = require("json")
  local file = require("file")
  local http = require("http")

  local repo = ctx.tool
  local version = ctx.version
  local install_path = ctx.install_path
  local download_path = ctx.download_path
  local options = ctx.options

  local function find_release(repo, version)
    local url
    if version == "latest" then
      url = "https://api.github.com/repos/" .. repo .. "/releases/latest"
    else
      url = "https://api.github.com/repos/" .. repo .. "/releases/tags/v" .. version
    end
    local resp, err = http.get({ url = url })
    if err == nil then
      local release = json.decode(resp.body)
      if type(release) == "table" and release.assets then
        return release
      end
    end
    -- fallback for pinned versions: list releases and match v(%d[%w%._+-]*)$
    -- ("latest" intentionally never matches here, so a bare "latest" that
    -- reaches the fallback still fails closed)
    local list, lerr = http.get({
      url = "https://api.github.com/repos/" .. repo .. "/releases?per_page=100",
    })
    if lerr ~= nil then
      error("failed to fetch releases: " .. lerr)
    end
    local releases = json.decode(list.body)
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
      if a.name:match(options.asset_pattern) then
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
        local body, herr = http.get({ url = a.browser_download_url })
        if herr == nil and body and body.body then
          for line in body.body:gmatch("[^\r\n]+") do
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

  local expected = options.sha256
  if type(expected) == "table" then
    expected = expected[RUNTIME.archType]
    -- mise reports aarch64 hosts as "arm64", so also honor the schema's
    -- "aarch64" key (schema validation forbids "arm64" as an arch key).
    if not expected and RUNTIME.archType == "arm64" then
      expected = expected["aarch64"]
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
  local dload_err = http.download_file({ url = asset.browser_download_url }, appimage)
  if dload_err ~= nil then
    error("download failed: " .. dload_err)
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
  local xdg = file.join_path(install_path, "app", "usr", "bin", "xdg-open")
  cmd.exec("[ -f " .. shq(xdg) .. " ] && [ -f /usr/local/bin/xdg-open ] && mv " .. shq(xdg) .. " " .. shq(xdg .. ".real") .. " && cp /usr/local/bin/xdg-open " .. shq(xdg) .. " && chmod +x " .. shq(xdg) .. " || true")

  local exe = options.exe or "AppRun"
  local name = options.name or repo:match("([^/]+)$")
  if has_control(exe) or has_control(name) then
    error("appimage: invalid exe/name option for " .. repo)
  end
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
