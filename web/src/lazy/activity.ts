import type { Match } from '../lib/routes';
import Audit from '../screens/activity/Audit.svelte';
import Releases from '../screens/activity/Releases.svelte';
import ScheduleForm from '../screens/activity/ScheduleForm.svelte';
import Schedules from '../screens/activity/Schedules.svelte';
import Spending from '../screens/activity/Spending.svelte';
import TaskDetail from '../screens/activity/TaskDetail.svelte';
import Tasks from '../screens/activity/Tasks.svelte';
import type { Screen } from '.';

const screens: Partial<Record<string, Screen>> = {
  '/activity': Tasks,
  '/activity/tasks/:id': TaskDetail,
  '/activity/schedules': Schedules,
  '/activity/schedules/:id': ScheduleForm,
  '/activity/releases': Releases,
  '/activity/spending': Spending,
  '/activity/audit': Audit,
};

export function screen(m: Match): Screen | null {
  return screens[m.pattern] ?? null;
}
