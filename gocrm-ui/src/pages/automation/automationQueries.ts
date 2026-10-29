import type { QueryClient } from '@tanstack/react-query';
import type { AutomationRuleFilters, AutomationRunFilters } from '@/api/endpoints/automation';
import type { NotificationFilters } from '@/api/endpoints/notifications';
import type { AutomationRule, AutomationRun } from '@/types';

// The single source of the automation and notification query keys. Lists
// share a prefix so one invalidation reaches every page, filter and record
// section that holds them.
export const automationKeys = {
  rules: ['automation', 'rules'] as const, // prefix of every rule list
  ruleList: (f: AutomationRuleFilters) => ['automation', 'rules', f] as const,
  rule: (id: number) => ['automation', 'rule', id] as const,
  runs: ['automation', 'runs'] as const, // prefix of run lists AND record sections
  runList: (f: AutomationRunFilters) => ['automation', 'runs', f] as const,
  runDetails: ['automation', 'run'] as const, // prefix of every run detail
  run: (id: number) => ['automation', 'run', id] as const,
};

export const notificationKeys = {
  all: ['notifications'] as const,
  unreadCount: ['notifications', 'unread-count'] as const,
  lists: ['notifications', 'list'] as const,
  list: (f: NotificationFilters) => ['notifications', 'list', f] as const,
};

// The app caches for five minutes and never refetches on focus, so every
// mutation marks what it changed stale here. An invalidated query without an
// observer refetches on its next mount.

const invalidateRunCaches = (qc: QueryClient) =>
  Promise.all([
    qc.invalidateQueries({ queryKey: automationKeys.runs }),
    qc.invalidateQueries({ queryKey: automationKeys.runDetails }),
  ]);

export const afterRuleCreated = async (qc: QueryClient, rule: AutomationRule): Promise<void> => {
  qc.setQueryData(automationKeys.rule(rule.id), rule);
  await qc.invalidateQueries({ queryKey: automationKeys.rules });
};

// An update or a disable cancels the rule's active runs (rule_changed,
// rule_disabled), so every run list, record section and run detail is stale.
export const afterRuleChanged = async (qc: QueryClient, rule: AutomationRule): Promise<void> => {
  qc.setQueryData(automationKeys.rule(rule.id), rule);
  await Promise.all([
    qc.invalidateQueries({ queryKey: automationKeys.rules }),
    invalidateRunCaches(qc),
  ]);
};

// Enabling starts nothing retroactively: no run changes.
export const afterRuleEnabled = async (qc: QueryClient, rule: AutomationRule): Promise<void> => {
  qc.setQueryData(automationKeys.rule(rule.id), rule);
  await qc.invalidateQueries({ queryKey: automationKeys.rules });
};

export const afterRuleDeleted = async (qc: QueryClient, id: number): Promise<void> => {
  qc.removeQueries({ queryKey: automationKeys.rule(id), exact: true });
  await Promise.all([
    qc.invalidateQueries({ queryKey: automationKeys.rules }),
    invalidateRunCaches(qc),
  ]);
};

export const afterRunCancelled = async (qc: QueryClient, run: AutomationRun): Promise<void> => {
  qc.setQueryData(automationKeys.run(run.id), run);
  await qc.invalidateQueries({ queryKey: automationKeys.runs });
};

// A cancel refused because the run finished meanwhile (409): the run moved on
// without this client, so its detail and every list or record section holding
// it are stale.
export const afterRunSettled = async (qc: QueryClient, runId: number): Promise<void> => {
  await Promise.all([
    qc.invalidateQueries({ queryKey: automationKeys.run(runId), exact: true }),
    qc.invalidateQueries({ queryKey: automationKeys.runs }),
  ]);
};

// Covers the unread count, the bell menu and the notifications page.
export const afterNotificationsRead = async (qc: QueryClient): Promise<void> => {
  await qc.invalidateQueries({ queryKey: notificationKeys.all });
};
