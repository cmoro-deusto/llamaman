package main

// Completion scripts emitted by `llamaman --completion <shell>`. These are
// hand-rolled rather than generated, so they remain readable and portable
// across kong versions. They invoke `llamaman --list` to enumerate model
// aliases for positional completion.

const bashCompletionScript = `# llamaman bash completion
_llamaman() {
  local cur prev words cword opts aliases
  COMPREPLY=()
  cur="${COMP_WORDS[COMP_CWORD]}"
  prev="${COMP_WORDS[COMP_CWORD-1]}"
  opts="-h --help --version -l --list -p --presets -c --config --completion"
  if [[ "$cur" == -* ]]; then
    COMPREPLY=( $(compgen -W "${opts}" -- "$cur") )
    return 0
  fi
  case "$prev" in
    -c|--config)
      COMPREPLY=( $(compgen -f -- "$cur") )
      return 0
      ;;
    --completion)
      COMPREPLY=( $(compgen -W "bash zsh fish" -- "$cur") )
      return 0
      ;;
  esac
  if [[ ${COMP_CWORD} -eq 1 || ( ${COMP_CWORD} -eq 2 && "$prev" == -p ) || ( ${COMP_CWORD} -eq 2 && "$prev" == --presets ) ]]; then
    aliases="$(llamaman --list 2>/dev/null | awk -F'\t' '{print $1}')"
    COMPREPLY=( $(compgen -W "${aliases}" -- "$cur") )
    return 0
  fi
}
complete -F _llamaman llamaman
`

const zshCompletionScript = `#compdef llamaman

_llamaman() {
  local -a aliases
  _arguments \
    '(-h --help)'{-h,--help}'[Show help]' \
    '--version[Print version and exit]' \
    '(-l --list)'{-l,--list}'[List configured models]' \
    '(-p --presets)'{-p,--presets}'[Print presets for alias]' \
    '(-c --config)'{-c,--config}'[Path to alternate config]:config:_files' \
    '--completion[Print completion script]:shell:(bash zsh fish)' \
    '*::arg:->args'
  case $state in
    args)
      aliases=("${(@f)$(llamaman --list 2>/dev/null | awk -F'\t' '{print $1}')}")
      _describe 'alias' aliases
      ;;
  esac
}
_llamaman "$@"
`

const fishCompletionScript = `# llamaman fish completion
function __llamaman_aliases
    llamaman --list 2>/dev/null | awk -F'\t' '{print $1}'
end

complete -c llamaman -f
complete -c llamaman -s h -l help        -d 'Show help'
complete -c llamaman      -l version     -d 'Print version and exit'
complete -c llamaman -s l -l list        -d 'List configured models'
complete -c llamaman -s p -l presets     -d 'Print presets for alias'
complete -c llamaman -s c -l config      -d 'Config file path' -r -F
complete -c llamaman      -l completion  -d 'Print completion script' -xa 'bash zsh fish'
complete -c llamaman -n '__fish_use_subcommand'                       -xa '(__llamaman_aliases)'
complete -c llamaman -n '__fish_seen_subcommand_from (__llamaman_aliases)' -f
`
