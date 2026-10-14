import type { Match } from '../lib/routes';
import KnowledgeSearch from '../screens/knowledge/KnowledgeSearch.svelte';
import Memory from '../screens/knowledge/Memory.svelte';
import ProjectNotes from '../screens/knowledge/ProjectNotes.svelte';
import Projects from '../screens/knowledge/Projects.svelte';
import SkillView from '../screens/knowledge/SkillView.svelte';
import Skills from '../screens/knowledge/Skills.svelte';
import type { Screen } from '.';

const screens: Partial<Record<string, Screen>> = {
  '/knowledge': Projects,
  '/knowledge/search': KnowledgeSearch,
  '/knowledge/memory': Memory,
  '/knowledge/skills': Skills,
  '/knowledge/skills/:name': SkillView,
  '/knowledge/projects/:owner/:repo': ProjectNotes,
};

export function screen(m: Match): Screen | null {
  return screens[m.pattern] ?? null;
}
