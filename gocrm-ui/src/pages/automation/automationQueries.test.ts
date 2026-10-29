import { describe, it, expect } from 'vitest';
import type { QueryClient } from '@tanstack/react-query';
import {
  afterNotificationsRead,
  afterRuleChanged,
  afterRuleCreated,
  afterRuleDeleted,
  afterRuleEnabled,
  afterRunCancelled,
  automationKeys,
  notificationKeys,
} from './automationQueries';
import { createProductionQueryClient } from '@/test/productionQueryClient';
import { isInvalidated } from '@/test/dealQueryCache';
import { createMockAutomationRule, createMockAutomationRun } from '@/test/factories';

const ruleListKey = automationKeys.ruleList({ offset: 0, limit: 20 });
const otherRuleListKey = automationKeys.ruleList({ offset: 20, limit: 20, sort_by: 'name' });
const runListKey = automationKeys.runList({ offset: 0, limit: 20 });
const recordRunsKey = automationKeys.runList({ entity_type: 'lead', entity_id: 5, limit: 5 });
const runDetailKey = automationKeys.run(31);
const ruleDetailKey = automationKeys.rule(7);
const otherRuleDetailKey = automationKeys.rule(8);

// Every cache a screen may hold, seeded as if each screen had been visited.
const seededClient = (): QueryClient => {
  const client = createProductionQueryClient();
  client.setQueryData(ruleListKey, { rules: [], total: 0 });
  client.setQueryData(otherRuleListKey, { rules: [], total: 0 });
  client.setQueryData(runListKey, { runs: [], total: 0 });
  client.setQueryData(recordRunsKey, { runs: [], total: 0 });
  client.setQueryData(runDetailKey, createMockAutomationRun({ id: 31 }));
  client.setQueryData(ruleDetailKey, createMockAutomationRule({ id: 7, version: 1 }));
  client.setQueryData(otherRuleDetailKey, createMockAutomationRule({ id: 8 }));
  client.setQueryData(notificationKeys.unreadCount, 2);
  return client;
};

const invalidatedRuleLists = (client: QueryClient) =>
  [ruleListKey, otherRuleListKey].map((key) => isInvalidated(client, key));
const invalidatedRunCaches = (client: QueryClient) =>
  [runListKey, recordRunsKey, runDetailKey].map((key) => isInvalidated(client, key));

describe('automationQueries', () => {
  it('after a rule create, invalidates every rule list only', async () => {
    const client = seededClient();
    const rule = createMockAutomationRule({ id: 9 });

    await afterRuleCreated(client, rule);

    expect(client.getQueryData(automationKeys.rule(9))).toEqual(rule);
    expect(invalidatedRuleLists(client)).toEqual([true, true]);
    expect(invalidatedRunCaches(client)).toEqual([false, false, false]);
    expect(isInvalidated(client, ruleDetailKey)).toBe(false);
    expect(isInvalidated(client, notificationKeys.unreadCount)).toBe(false);
  });

  it('after a rule update or disable, stores the rule and invalidates rule lists, run lists and run details', async () => {
    const client = seededClient();
    const rule = createMockAutomationRule({ id: 7, version: 2, enabled: false });

    await afterRuleChanged(client, rule);

    expect(client.getQueryData(ruleDetailKey)).toEqual(rule);
    expect(isInvalidated(client, ruleDetailKey)).toBe(false);
    expect(invalidatedRuleLists(client)).toEqual([true, true]);
    expect(invalidatedRunCaches(client)).toEqual([true, true, true]);
    expect(isInvalidated(client, otherRuleDetailKey)).toBe(false);
    expect(isInvalidated(client, notificationKeys.unreadCount)).toBe(false);
  });

  it('after a rule enable, stores the rule and invalidates rule lists only', async () => {
    const client = seededClient();
    const rule = createMockAutomationRule({ id: 7, enabled: true });

    await afterRuleEnabled(client, rule);

    expect(client.getQueryData(ruleDetailKey)).toEqual(rule);
    expect(invalidatedRuleLists(client)).toEqual([true, true]);
    expect(invalidatedRunCaches(client)).toEqual([false, false, false]);
  });

  it('after a rule delete, removes the rule detail and invalidates rule lists, run lists and run details', async () => {
    const client = seededClient();

    await afterRuleDeleted(client, 7);

    expect(client.getQueryState(ruleDetailKey)).toBeUndefined();
    expect(client.getQueryData(otherRuleDetailKey)).toBeDefined();
    expect(invalidatedRuleLists(client)).toEqual([true, true]);
    expect(invalidatedRunCaches(client)).toEqual([true, true, true]);
  });

  it('after a run cancel, stores the run and invalidates run lists', async () => {
    const client = seededClient();
    const run = createMockAutomationRun({ id: 31, status: 'cancelled', cancel_reason: 'manual' });

    await afterRunCancelled(client, run);

    expect(client.getQueryData(runDetailKey)).toEqual(run);
    expect(isInvalidated(client, runDetailKey)).toBe(false);
    expect([runListKey, recordRunsKey].map((key) => isInvalidated(client, key))).toEqual([
      true,
      true,
    ]);
    expect(invalidatedRuleLists(client)).toEqual([false, false]);
  });

  it('after a notification read, invalidates the count, the menu and the page', async () => {
    const client = seededClient();
    const menuKey = notificationKeys.list({ offset: 0, limit: 5 });
    const pageKey = notificationKeys.list({ offset: 0, limit: 20, unread: true });
    client.setQueryData(menuKey, { notifications: [], total: 0 });
    client.setQueryData(pageKey, { notifications: [], total: 0 });

    await afterNotificationsRead(client);

    expect(
      [notificationKeys.unreadCount, menuKey, pageKey].map((key) => isInvalidated(client, key)),
    ).toEqual([true, true, true]);
    expect(invalidatedRuleLists(client)).toEqual([false, false]);
    expect(invalidatedRunCaches(client)).toEqual([false, false, false]);
  });
});
