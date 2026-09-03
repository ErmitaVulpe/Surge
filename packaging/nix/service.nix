# Non-module dependencies (`importApply`)
{ formats }:

# Service module
{
  config,
  lib,
  options,
  ...
}:
let
  cfg = config.surge;
  toml = formats.toml { };
  json = formats.json { };
in
{
  _class = "service";

  meta.maintainers = with lib.maintainers; [ ErmitaVulpe ];

  options.surge = {
    package = lib.mkOption {
      description = "Package to use for surge";
      defaultText = "The package that provided this module.";
      type = lib.types.package;
    };

    settings = lib.mkOption {
      type = lib.types.nullOr toml.type;
      description = "Configuration for Surge, as defined in https://github.com/SurgeDM/Surge/blob/main/docs/SETTINGS.md";
      default = null;
      example = {
        general = {
          default_download_dir = "/path/to/downloads";
          theme = 2;
        };
        network = {
          max_connections_per_host = 16;
        };
        performance = {
          max_task_retries = 5;
        };
        categories = {
          category_enabled = true;
        };
      };
    };

    keymap = lib.mkOption {
      type = lib.types.nullOr json.type;
      description = "Keymap configuration for Surge";
      default = null;
      example = {
        dashboard = {
          Quit = {
            keys = [
              "ctrl+c"
              "ctrl+q"
            ];
            help = "quit";
          };
          Up = {
            keys = [
              "up"
              "k"
            ];
            help = "up";
          };
        };
      };
    };

    headless = {
      token = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        description = "Api token for the server. If null, will use an auto-generated. To see what it is, run `surge token`";
        default = null;
      };

      extraArgs = lib.mkOption {
        type = lib.types.listOf lib.types.str;
        default = [ ];
        description = "Extra arguments to pass to `surge server`";
      };
    };
  };

  config = {
    process = {
      argv = [
        (lib.getExe cfg.package)
        "server"
        "start"
      ]
      ++ lib.optionals (cfg.headless.token != null) [
        "--token"
        cfg.token
      ]
      ++ cfg.headless.extraArgs;
    };

    configData = {
      "settings.toml" = lib.mkIf (!isNull cfg.settings) {
        source = toml.generate "settings.toml" cfg.settings;
      };
      "keymap.json" = lib.mkIf (!isNull cfg.keymap) {
        source = json.generate "keymap.json" cfg.keymap;
      };
    };
  }
  // lib.optionalAttrs (options ? systemd) (
    let
      serviceCapabilities = [ "" ];
    in
    {
      systemd.mainExecStart = config.systemd.lib.escapeSystemdExecArgs config.process.argv;

      systemd.service = {
        description = "Surge downloader headless server";

        wantedBy = [ "multi-user.target" ];
        after = [ "network-online.target" ];
        wants = [ "network-online.target" ];

        serviceConfig = {
          Restart = "on-failure";
          ProtectSystem = "full";

          # Hardening
          MemoryDenyWriteExecute = true;
          NoNewPrivileges = true;
          CapabilityBoundingSet = serviceCapabilities;
          AmbientCapabilities = serviceCapabilities;
          SystemCallFilter = "@system-service";
          ProtectProc = "noaccess";
          PrivateTmp = "yes";
        };
      };
    }
  );
}
