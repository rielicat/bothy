import { describe, expect, it } from 'vitest';
import { router, safeNext } from './router.svelte';

describe('router', () => {
  it('navigates within the origin and resolves the route', () => {
    router.navigate('/chat/ses_01?x=1');
    expect(location.pathname).toBe('/chat/ses_01');
    expect(router.current).toEqual({ pattern: '/chat/:id', params: { id: 'ses_01' } });
    expect(router.query.get('x')).toBe('1');
  });

  it('refuses to navigate to another origin', () => {
    router.navigate('/usage');
    router.navigate('https://evil.example/usage');
    expect(location.origin).not.toBe('https://evil.example');
    expect(router.path).toBe('/usage');
  });

  it('merges query parameters and drops empty ones', () => {
    router.navigate('/approvals?status=recent&x=1');
    router.setQuery({ status: null, page: '2' });
    expect(router.search).toBe('?x=1&page=2');
  });

  it('follows popstate', () => {
    router.navigate('/memory');
    history.pushState(null, '', '/audit?tab=egress');
    dispatchEvent(new PopStateEvent('popstate'));
    expect(router.current?.pattern).toBe('/audit');
    expect(router.query.get('tab')).toBe('egress');
  });

  it('intercepts plain left clicks on same-origin links only', () => {
    const a = document.createElement('a');
    a.href = '/schedules';
    a.addEventListener('click', router.link);
    document.body.append(a);

    const modified = new MouseEvent('click', { bubbles: true, cancelable: true, metaKey: true });
    a.dispatchEvent(modified);
    expect(modified.defaultPrevented).toBe(false);

    const plain = new MouseEvent('click', { bubbles: true, cancelable: true });
    a.dispatchEvent(plain);
    expect(plain.defaultPrevented).toBe(true);
    expect(router.path).toBe('/schedules');

    a.href = 'https://elsewhere.example/x';
    const external = new MouseEvent('click', { bubbles: true, cancelable: true });
    a.dispatchEvent(external);
    expect(external.defaultPrevented).toBe(false);
    a.remove();
  });

  it('only accepts same-origin paths for ?next=', () => {
    expect(safeNext('/chat/ses_1?x=1')).toBe('/chat/ses_1?x=1');
    expect(safeNext('//evil.example')).toBe('/chat');
    expect(safeNext('/\\evil.example')).toBe('/chat');
    expect(safeNext('/\t/evil.example')).toBe('/chat');
    expect(safeNext('/\n/evil.example')).toBe('/chat');
    expect(safeNext('https://evil.example')).toBe('/chat');
    expect(safeNext('javascript:alert(1)')).toBe('/chat');
    expect(safeNext(null, '/usage')).toBe('/usage');
  });
});
