import type { Component } from 'svelte';
import ChatApp from './ChatApp.svelte';
import Connections from './Connections.svelte';
import { isApp } from './connections';
import GitHub from './GitHub.svelte';
import McpServer from './McpServer.svelte';
import WebSearch from './WebSearch.svelte';

/**
 * The screen for /settings/connections and /settings/connections/:item
 * (docs/design/ui.md §4.5), or null when the item names nothing. App routes
 * the whole group through here, so new Connections pages need no App change.
 */
export function connectionsScreen(item?: string): Component<any> | null {
  if (item === undefined) return Connections;
  if (isApp(item)) return ChatApp;
  if (item === 'github') return GitHub;
  if (item === 'search') return WebSearch;
  if (/^mcp-[a-z0-9][a-z0-9-]*$/.test(item)) return McpServer;
  return null;
}
