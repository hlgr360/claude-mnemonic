#!/bin/bash
# Unregister claude-mnemonic plugin from Claude Code
#
# Usage: unregister-plugin.sh [--purge]
#   --purge   also delete ~/.claude-mnemonic: the database, settings and embeddings. Without it your data is kept:
#             unregistering the plugin (to switch to another install route, say) is not deleting your memory.

set -e

PURGE=false
for arg in "$@"; do
    case "$arg" in
        --purge) PURGE=true ;;
        *)
            echo "Usage: $0 [--purge]   (--purge also deletes ~/.claude-mnemonic: the database, settings and embeddings)" >&2
            exit 2
            ;;
    esac
done

# Stop running worker processes before removing binaries
echo "Stopping worker processes..."
pkill -TERM -f 'claude-mnemonic.*worker' 2>/dev/null || true
pkill -TERM -f '\.claude/plugins/.*/worker' 2>/dev/null || true
sleep 2
# Force kill if still running
pkill -9 -f 'claude-mnemonic.*worker' 2>/dev/null || true
pkill -9 -f '\.claude/plugins/.*/worker' 2>/dev/null || true
# Clean up port
lsof -ti :37777 | xargs kill -9 2>/dev/null || true
sleep 1

PLUGINS_FILE="$HOME/.claude/plugins/installed_plugins.json"
SETTINGS_FILE="$HOME/.claude/settings.json"
MARKETPLACES_FILE="$HOME/.claude/plugins/known_marketplaces.json"
# The marketplace name register-plugin.sh used (MNEMONIC_MARKETPLACE, default claude-mnemonic)
MARKETPLACE_NAME="${MNEMONIC_MARKETPLACE:-claude-mnemonic}"
CACHE_DIR="$HOME/.claude/plugins/cache/${MARKETPLACE_NAME}"
PLUGIN_KEY="claude-mnemonic@${MARKETPLACE_NAME}"

# Check if jq is available
if ! command -v jq &> /dev/null; then
    echo "Warning: jq not found, please manually remove plugin entries from:"
    echo "  - $PLUGINS_FILE (remove $PLUGIN_KEY)"
    echo "  - $SETTINGS_FILE (remove from enabledPlugins and statusLine)"
    echo "  - $MARKETPLACES_FILE (remove $MARKETPLACE_NAME)"
    echo "  - $CACHE_DIR (remove directory)"
    echo "  - $HOME/.claude-mnemonic is your data (database, settings, embeddings): leave it unless you want it gone"
    exit 1
fi

# Remove from installed_plugins.json
if [ -f "$PLUGINS_FILE" ]; then
    jq --arg key "$PLUGIN_KEY" 'del(.plugins[$key])' "$PLUGINS_FILE" > "${PLUGINS_FILE}.tmp" \
        && mv "${PLUGINS_FILE}.tmp" "$PLUGINS_FILE"
    echo "Plugin removed from installed_plugins.json"
else
    echo "No plugins file found, skipping"
fi

# Remove from settings.json (enabledPlugins, statusLine, and mcpServers)
if [ -f "$SETTINGS_FILE" ]; then
    # Remove from enabledPlugins, clear statusLine if it references our plugin, and remove MCP server
    jq --arg key "$PLUGIN_KEY" '
        del(.enabledPlugins[$key]) |
        if .statusLine.command and (.statusLine.command | contains("claude-mnemonic")) then
            del(.statusLine)
        else
            .
        end |
        del(.mcpServers["claude-mnemonic"])
    ' "$SETTINGS_FILE" > "${SETTINGS_FILE}.tmp" \
        && mv "${SETTINGS_FILE}.tmp" "$SETTINGS_FILE"
    echo "Plugin removed from settings.json"
fi

# Remove from known_marketplaces.json
if [ -f "$MARKETPLACES_FILE" ]; then
    jq --arg key "$MARKETPLACE_NAME" 'del(.[$key])' "$MARKETPLACES_FILE" > "${MARKETPLACES_FILE}.tmp" \
        && mv "${MARKETPLACES_FILE}.tmp" "$MARKETPLACES_FILE"
    echo "Marketplace removed from known_marketplaces.json"
fi

# Remove cache directory
if [ -d "$CACHE_DIR" ]; then
    rm -rf "$CACHE_DIR"
    echo "Cache directory removed"
fi

# The data directory (database, embeddings, settings) is kept unless --purge was given.
DATA_DIR="$HOME/.claude-mnemonic"
if [ -d "$DATA_DIR" ]; then
    if [ "$PURGE" = true ]; then
        rm -rf "$DATA_DIR"
        echo "Data directory removed ($DATA_DIR)"
    else
        echo "Data directory kept ($DATA_DIR); to delete it as well, run: $0 --purge"
    fi
fi

echo "Plugin unregistered successfully"
