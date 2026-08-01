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

  local resp, err = http.get({
    url = "https://api.github.com/repos/" .. repo .. "/releases/tags/v" .. version,
  })
  if err ~= nil then
    error("failed to fetch release: " .. err)
  end
  local release = json.decode(resp.body)
  if type(release) ~= "table" or not release.assets then
    error("no release found for " .. repo .. "@v" .. version)
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

  local appimage = file.join_path(download_path, "app.AppImage")
  local dload_err = http.download_file({ url = asset.browser_download_url }, appimage)
  if dload_err ~= nil then
    error("download failed: " .. dload_err)
  end
  cmd.exec("chmod +x " .. appimage)

  cmd.exec("cd " .. download_path .. " && ./app.AppImage --appimage-extract >/dev/null")

  cmd.exec("mkdir -p " .. file.join_path(install_path, "app"))
  cmd.exec("cp -a " .. download_path .. "/squashfs-root/." .. " " .. file.join_path(install_path, "app") .. "/")

  local exe = options.exe or "AppRun"
  local name = options.name or repo:match("([^/]+)$")
  local launcher = '#!/usr/bin/env bash\nexec "$(dirname "$0")/../app/' .. exe .. '" "$@"\n'
  cmd.exec("mkdir -p " .. file.join_path(install_path, "bin"))
  cmd.exec("cat > " .. file.join_path(install_path, "bin", name) .. " <<'APPIMAGE_EOF'\n" .. launcher .. "APPIMAGE_EOF")
  cmd.exec("chmod +x " .. file.join_path(install_path, "bin", name))

  return {}
end
