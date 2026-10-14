// The attention dot (docs/design/ui.md §4.10): Security for findings and
// running-code problems, Connections for a disconnected chat app or a tool
// waiting for review. Anything that can't be read leaves its dot off.
import { api } from '../../lib/api/client';
import type { ChannelStatus, DoctorReport, MCPStatus } from '../../lib/api/types.gen';
import { events } from '../../lib/sse.svelte';
import { session } from '../../lib/stores/session.svelte';

const brokenChannel = (c: ChannelStatus) => c.enabled && (c.state === 'auth_failed' || c.state === 'backoff');
const toolNeedsLook = (m: MCPStatus) => m.pending > 0 || m.state === 'pins_pending' || m.state === 'error';
const has = (...codes: string[]) => session.banners.some((b) => codes.includes(b.code));

class Attention {
  findings = $state(false);
  channels = $state(false);
  tools = $state(false);

  security = $derived(session.isAdmin && (this.findings || session.sandbox === false || has('no_sandbox', 'sandbox_not_isolated')));
  connections = $derived(session.isAdmin && (this.channels || this.tools || has('mcp_pins', 'mcp_drifted')));
  any = $derived(this.security || this.connections);

  async refresh() {
    if (!session.isAdmin) return;
    const [audit, channels, tools] = await Promise.allSettled([
      api.post<DoctorReport>('/api/security-audit', { fix: false }),
      api.get<ChannelStatus[]>('/api/channels'),
      api.get<MCPStatus[]>('/api/mcp'),
    ]);
    this.findings = audit.status === 'fulfilled' && (audit.value?.findings ?? []).some((f) => f.security);
    this.channels = channels.status === 'fulfilled' && (channels.value ?? []).some(brokenChannel);
    this.tools = tools.status === 'fulfilled' && (tools.value ?? []).some(toolNeedsLook);
  }

  listen(): () => void {
    let timer: ReturnType<typeof setTimeout> | undefined;
    const soon = () => {
      clearTimeout(timer);
      timer = setTimeout(() => void this.refresh(), 1000);
    };
    const offs = [events.on('channel.status', soon), events.on('mcp.pin_pending', soon), events.on('sandbox.status', soon), events.on('config.changed', soon), events.onResync(soon)];
    return () => {
      clearTimeout(timer);
      offs.forEach((f) => f());
    };
  }
}

export const attention = new Attention();
