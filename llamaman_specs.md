# llamaman utility

i want to create a command line utility for linux. the utility is called llamaman (llama-server manager). it can manage models and their presets so i can launch llama-server easily. it can also create, edit or delete existing model configurations. 



## Configuration

it should use a config file stored in proper standard place (i guess ~/.config/llamaman/config.json).

in the config, we should have a globals object where we can setup the llama-server binary path, the ip adress where llama-server will listen (127.0.0.1 as default) and the llama-server port (9080 as default). 

then it should have a models object. each model object entry must contain: location with the model filename, an alias, and a presets array of objects. 

the presets should have a name (preset entry), a description entry, and a params entry. 

the params entry contains key-value pairs of optional entries for each of llama-server available launch options (ie: temp, top-p, top-k, jinja, ctx-size, parallel, batch-size, ubatch-size, cache-type-k, cache-type-v...). hint: you can get the full list of valid values for the launch options by executing 'llama-server --help' and reading its output. See that llama-server usually supports both short and long form options, we need to support both. 

the presets array itself can be optionally empty.


draft of config.json:

```
{
  "globals": {
    "llama-server-bin": "/usr/local/llama.cpp/bin/llama-server",
    "ip_address": "127.0.0.1",
    "port": 9080
  },
  "models": [
    {
      "alias": "Qwen3.5-35B-A3B-Q4_K_M.gguf",
      "location": "Qwen3.5-35B-A3B-Q4_K_M.gguf",
      "presets": [
        {
          "preset": "default",
          "description": "",
          "params": {
            "temp": 1.0,
            "top-p": 0.95,
            "top-k": 64
          }
        }
      ]
    },
    {
      "alias": "Qwen3.6-35B-A3B-Q4_K_M.gguf",
      "location": "Qwen3.6-35B-A3B-Q4_K_M.gguf",
      "presets": [
        {
          "preset": "default",
          "description": "",
          "params": {
            "ctx-size": 16384,
            "temp": 0.6,
            "top-p": 0.95,
            "top-k": 20,
            "min-p": 0.00
          }
        },
        {
          "preset": "bigcontext",
          "description": "",
          "params": {
            "ctx-size": 200000,
            "temp": 1.0,
            "top-p": 0.95,
            "top-k": 20,
            "min-p": 0.00
          }
        }
      ]
    }
  ]
}
```


when llamaman is launched, it should look for its config size in the standard location and parse it. i should be also able to override the config file location if i launch 'llamaman -config <config file>'. 

if llamaman is launched without --config and no config file is found in the standard location, a example config.json file should be created using defaults. the models   
object in the example config.json file should include a few example model entries with a couple of presets.


### Usage from the commandline

```
llamaman [OPTIONS] <model_alias> [preset]
```

Available options should be at least:

help (-h or --help): display full usage instructions. it should show a first line like "llamaman vX.Y.Z llama-server manager"
list (-l or --list): list all available model configurations
presets (-p or --preset): show preset info for a given model alias
config (-c or --config): location for the configuration file to override default global location

if launched without options but with a model_alias, it should run llama-server model-alias default preset. if launched without options, a model_alias and a preset name, it should run llama-server with the given model_alias specific preset.

### Hint for implementation

if i have this config:

```
{
  "globals": {
    "llama-server-bin": "/usr/local/llama.cpp/bin/llama-server",
    "ip_address": "127.0.0.1",
    "port": 9080
  },
  "models": [
    {
      "alias": "qwen3.6-27B",
      "location": "~/Code/ai/models/Qwen3.6-27B-Q4_K_XL.gguf",
      "presets": [
        {
          "preset": "default",
          "description": "",
          "params": {
            "ngl": 99
            "ctx-size": 262144,
            "parallel": 1,
            "batch-size": 2048,
            "ubatch-size": 256,
            "ctk": "q4_0",
            "ctv": "q4_0",
            "fa": "on",
            "presence-penalty": 0.0
            "temp": 0.6,
            "top-p": 0.95,
            "top-k": 20,
            "min-p": 0.00,
            "chat-template-kwargs": "'{"preserve_thinking": true}'"
            "jinja": true,
            "no-mmproj": true,
            "metrics": true
          }
        },
        {
          "preset": "smallctx",
          "description": "",
          "params": {
            "ngl": 99
            "ctx-size": 32000,
            "parallel": 1,
            "batch-size": 2048,
            "ubatch-size": 256,
            "ctk": "q4_0",
            "ctv": "q4_0",
            "fa": "on",
            "presence-penalty": 0.0
            "temp": 0.6,
            "top-p": 0.95,
            "top-k": 20,
            "min-p": 0.00,
            "chat-template-kwargs": "'{"preserve_thinking": true}'"
            "jinja": true,
            "no-mmproj": true,
            "metrics": true
          }
        },        
      ]
    }
  ]
}

```
and run:


llamamann qwen3.6-27B


it should execute the following command:

```
llama-server -m ~/Code/ai/models/Qwen3.6-27B-Q4_K_XL.gguf --alias qwen3.6-27B --no-mmproj --jinja -ngl 99 --ctx-size 262144 --parallel 1 --batch-size 2048 --ubatch-size 256 --ctk q4_0 --ctv q4_0 --fa on --presence-penalty 0.0 --temp 0.6 --top-p 0.95 --top-k 20 --min-p 0.00 --chat-template-kwargs '{"preserve_thinking": true}' --port 9080 --metrics
```

if i run:

llamaman qwen3.6-27B smallctx

it should execute:

```
llama-server -m ~/Code/ai/models/Qwen3.6-27B-Q4_K_XL.gguf --alias qwen3.6-27B --no-mmproj --jinja -ngl 99 --ctx-size 32000 --parallel 1 --batch-size 2048 --ubatch-size 256 --ctk q4_0 --ctv q4_0 --fa on --presence-penalty 0.0 --temp 0.6 --top-p 0.95 --top-k 20 --min-p 0.00 --chat-template-kwargs '{"preserve_thinking": true}' --port 9080 --metrics
```


## Interface

The interface should be a fancy, modern TUI. 

It should support different modes:

  - main mode: shows when llamaman launched without any param. it should show a centered (vertically and horizontally) window. Inside the window, top side, llamaman ascii art. Below it, a list of available shortcuts with available options
  - selection mode: shows a list of preconfigured model aliases in a fancy list. the list should be navigable using arrow keys. when pressing enter, it should check if a model has more than one preset. if so, it should show a list of presets, also navigable with arrow keys, to select the model alias preset. either if a model alias has been selected that has no presets or only on preset, or if a model alias and a pressed have been selected, it should switch to run mode. there should be a shortcut to switch to configuration mode for the selected model alias/preset, maybe e for edit would be ok.
  - run mode: executes a llama-server command. It should have a top pane, full width, that has info about the current model preset being run. It should not occupy a lot of screen space, maybe 3-4 lines max. Below it, another window that should occupy the rest of available screen space. this window should show the llama-server execution output, which should be tailed.   
  - configuration mode: used to manage model aliases and presets configurations. it should be best in class TUI UX.

