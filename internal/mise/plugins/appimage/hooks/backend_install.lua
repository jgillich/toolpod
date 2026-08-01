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
    local resp, err = http.get({
      url = "https://api.github.com/repos/" .. repo .. "/releases/tags/v" .. version,
    })
    if err == nil then
      local release = json.decode(resp.body)
      if type(release) == "table" and release.assets then
        return release
      end
    end
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

  local appimage = file.join_path(download_path, "app.AppImage")
  local dload_err = http.download_file({ url = asset.browser_download_url }, appimage)
  if dload_err ~= nil then
    error("download failed: " .. dload_err)
  end
  cmd.exec("chmod +x " .. appimage)

  cmd.exec("cd " .. download_path .. " && ./app.AppImage --appimage-extract >/dev/null")

  cmd.exec("mkdir -p " .. file.join_path(install_path, "app"))
  cmd.exec("cp -a " .. download_path .. "/squashfs-root/." .. " " .. file.join_path(install_path, "app") .. "/")

  -- Swap the bundled xdg-open for the image wrapper that forwards URLs to the
  -- host's XDG desktop portal (AppRun prepends the bundle's usr/bin to PATH).
  local xdg = file.join_path(install_path, "app", "usr", "bin", "xdg-open")
  cmd.exec("[ -f " .. xdg .. " ] && [ -f /usr/local/bin/xdg-open ] && mv " .. xdg .. " " .. xdg .. ".real && cp /usr/local/bin/xdg-open " .. xdg .. " && chmod +x " .. xdg .. " || true")

  local exe = options.exe or "AppRun"
  local name = options.name or repo:match("([^/]+)$")
  local launcher = '#!/usr/bin/env bash\nexec "$(dirname "$0")/../app/' .. exe .. '" "$@"\n'
  cmd.exec("mkdir -p " .. file.join_path(install_path, "bin"))
  cmd.exec("cat > " .. file.join_path(install_path, "bin", name) .. " <<'APPIMAGE_EOF'\n" .. launcher .. "APPIMAGE_EOF")
  cmd.exec("chmod +x " .. file.join_path(install_path, "bin", name))

  return {}
end
