import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent, within } from '@/test/test-utils';
import { useNavigate, useParams } from 'react-router-dom';
import type { QueryClient } from '@tanstack/react-query';
import { Component as TaskForm } from './TaskForm';
import { dealsApi, labelsApi, tasksApi, usersApi } from '@/api/endpoints';
import { createMockDeal, createMockTask, createMockUser } from '@/test/factories';
import { pickAutocompleteOption } from '@/test/autocomplete';
import { createProductionQueryClient } from '@/test/productionQueryClient';
import { isInvalidated, withClient } from '@/test/dealQueryCache';
import type { Task, User } from '@/types';

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return { ...actual, useNavigate: vi.fn(), useParams: vi.fn() };
});

const mockUseAuth = vi.fn();
vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => mockUseAuth(),
}));

vi.mock('@/api/endpoints', () => ({
  tasksApi: {
    getTask: vi.fn(),
    createTask: vi.fn(),
    updateTask: vi.fn(),
  },
  usersApi: {
    getUsers: vi.fn(),
  },
  labelsApi: {
    getLabels: vi.fn(),
    createLabel: vi.fn(),
  },
  dealsApi: {
    getDeals: vi.fn(),
    getDeal: vi.fn(),
  },
  configurationsApi: {
    getUIConfigurations: vi.fn().mockResolvedValue([]),
  },
}));

const authState = (user: User) => ({
  user,
  isLoading: false,
  isAuthenticated: true,
  login: vi.fn(),
  register: vi.fn(),
  logout: vi.fn(),
  refreshUser: vi.fn(),
});

const asRole = (role: User['role']) =>
  mockUseAuth.mockReturnValue(authState(createMockUser({ id: 1, role })));

const WAIT = { timeout: 3000 };

const openDeal = createMockDeal({ id: 3, title: 'Acme renewal', stage: 'proposal' });
const wonDeal = createMockDeal({ id: 4, title: 'Acme expansion', stage: 'won' });
const linkedSummary = { id: 7, title: 'Website redesign', stage: 'negotiation' as const };

const linkedTask = (overrides?: Partial<Task>): Task =>
  createMockTask({ id: 5, title: 'Call the client', labels: [], deal_id: 7, deal: linkedSummary, ...overrides });

const deferred = <T,>() => {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
};

const editMode = () => vi.mocked(useParams).mockReturnValue({ id: '5' });
const createMode = () => vi.mocked(useParams).mockReturnValue({});

const renderForm = (client?: QueryClient) =>
  render(client ? withClient(client, <TaskForm />) : <TaskForm />);

const formReady = () => screen.findByLabelText('Labels', {}, WAIT);

const fillTitle = (value: string) =>
  fireEvent.change(screen.getByLabelText(/^Title/), { target: { value } });

const dealPicker = () => screen.getByTestId('task-deal-autocomplete');

const updatePayload = () => vi.mocked(tasksApi.updateTask).mock.calls[0][1];

describe('TaskForm', () => {
  const navigate = vi.fn();

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useNavigate).mockReturnValue(navigate);
    createMode();
    asRole('admin');
    vi.mocked(usersApi.getUsers).mockResolvedValue({
      data: [],
      total: 0,
      page: 1,
      limit: 100,
      total_pages: 0,
    });
    vi.mocked(labelsApi.getLabels).mockResolvedValue([]);
    vi.mocked(dealsApi.getDeals).mockResolvedValue({ deals: [openDeal, wonDeal], total: 2 });
    vi.mocked(dealsApi.getDeal).mockResolvedValue(openDeal);
    vi.mocked(tasksApi.getTask).mockResolvedValue(linkedTask());
    vi.mocked(tasksApi.createTask).mockResolvedValue(createMockTask({ id: 6 }));
    vi.mocked(tasksApi.updateTask).mockResolvedValue(linkedTask());
  });

  describe('deal link', () => {
    it('shows no deal picker to the support and customer roles', async () => {
      for (const role of ['support', 'customer'] as const) {
        asRole(role);

        // Creating.
        createMode();
        const created = renderForm();
        await formReady();
        expect(screen.queryByLabelText('Deal')).not.toBeInTheDocument();
        expect(screen.queryByTestId('task-deal-autocomplete')).not.toBeInTheDocument();
        created.unmount();

        // Editing a linked task: still no picker, and the save leaves the
        // link alone.
        editMode();
        const edited = renderForm();
        await formReady();
        await waitFor(() => expect(screen.getByLabelText(/^Title/)).toHaveValue('Call the client'));
        expect(screen.queryByLabelText('Deal')).not.toBeInTheDocument();
        expect(screen.queryByTestId('task-deal-autocomplete')).not.toBeInTheDocument();
        fireEvent.click(screen.getByRole('button', { name: 'Update Task' }));
        await waitFor(() => expect(tasksApi.updateTask).toHaveBeenCalledTimes(1));
        expect(updatePayload()).not.toHaveProperty('deal_id');
        await waitFor(() => expect(navigate).toHaveBeenCalledWith('/tasks/5'));
        edited.unmount();
        vi.mocked(tasksApi.updateTask).mockClear();
        navigate.mockClear();
      }

      expect(dealsApi.getDeals).not.toHaveBeenCalled();
      expect(dealsApi.getDeal).not.toHaveBeenCalled();
    });

    it('offers only the deals the API returned, closed deals included', async () => {
      renderForm();
      await formReady();

      const input = within(dealPicker()).getByLabelText('Deal');
      input.focus();
      fireEvent.mouseDown(input);

      await screen.findByRole('option', { name: 'Acme expansion' }, WAIT);
      const listbox = screen.getByRole('listbox');
      expect(within(listbox).getAllByRole('option').map((option) => option.textContent)).toEqual([
        'Acme renewal',
        'Acme expansion',
      ]);

      expect(dealsApi.getDeals).toHaveBeenCalled();
      for (const [filters] of vi.mocked(dealsApi.getDeals).mock.calls) {
        expect(filters).not.toHaveProperty('open');
        expect(filters).toMatchObject({ limit: 20 });
      }
    });

    it('sends the picked deal_id when a task is created', async () => {
      renderForm();
      await formReady();

      fillTitle('Prepare the contract');
      await pickAutocompleteOption('Deal', 'Acme expansion');
      fireEvent.click(screen.getByRole('button', { name: 'Create Task' }));

      await waitFor(() => expect(tasksApi.createTask).toHaveBeenCalledTimes(1));
      expect(vi.mocked(tasksApi.createTask).mock.calls[0][0]).toMatchObject({
        title: 'Prepare the contract',
        deal_id: 4,
      });
      await waitFor(() => expect(navigate).toHaveBeenCalledWith('/tasks'));
    });

    it('leaves deal_id out of the update when the picker is untouched', async () => {
      editMode();
      renderForm();
      await formReady();

      // Pre-filled from the summary, without a lookup.
      await waitFor(() =>
        expect(within(dealPicker()).getByLabelText('Deal')).toHaveValue('Website redesign')
      );
      expect(dealsApi.getDeal).not.toHaveBeenCalled();

      fireEvent.click(screen.getByRole('button', { name: 'Update Task' }));

      await waitFor(() => expect(tasksApi.updateTask).toHaveBeenCalledTimes(1));
      expect(vi.mocked(tasksApi.updateTask).mock.calls[0][0]).toBe(5);
      expect(updatePayload()).not.toHaveProperty('deal_id');
      await waitFor(() => expect(navigate).toHaveBeenCalledWith('/tasks/5'));
    });

    it('leaves deal_id out when a task with deal_id but no summary is saved untouched', async () => {
      editMode();
      vi.mocked(tasksApi.getTask).mockResolvedValue(linkedTask({ deal: undefined }));
      renderForm();
      await formReady();
      await waitFor(() => expect(screen.getByLabelText(/^Title/)).toHaveValue('Call the client'));

      // The picker starts empty and nothing is looked up for the bare id.
      expect(within(dealPicker()).getByLabelText('Deal')).toHaveValue('');
      expect(dealsApi.getDeal).not.toHaveBeenCalled();

      fireEvent.click(screen.getByRole('button', { name: 'Update Task' }));

      await waitFor(() => expect(tasksApi.updateTask).toHaveBeenCalledTimes(1));
      expect(updatePayload()).not.toHaveProperty('deal_id');
      await waitFor(() => expect(navigate).toHaveBeenCalledWith('/tasks/5'));
    });

    it('sends deal_id 0 on a clear and keeps the link the API returned on the re-read', async () => {
      editMode();
      const client = createProductionQueryClient();
      const reRead = deferred<Task>();
      vi.mocked(tasksApi.getTask)
        .mockResolvedValueOnce(linkedTask())
        .mockImplementationOnce(() => reRead.promise);
      // The PUT body claims the link is gone; the form must not trust it.
      vi.mocked(tasksApi.updateTask).mockResolvedValue(
        linkedTask({ deal_id: undefined, deal: undefined })
      );

      renderForm(client);
      await formReady();
      await waitFor(() =>
        expect(within(dealPicker()).getByLabelText('Deal')).toHaveValue('Website redesign')
      );

      fireEvent.click(within(dealPicker()).getByLabelText('Clear'));
      await waitFor(() => expect(within(dealPicker()).getByLabelText('Deal')).toHaveValue(''));
      fireEvent.click(screen.getByRole('button', { name: 'Update Task' }));

      await waitFor(() => expect(tasksApi.updateTask).toHaveBeenCalledTimes(1));
      expect(vi.mocked(tasksApi.updateTask).mock.calls[0][0]).toBe(5);
      expect(updatePayload()).toMatchObject({ deal_id: 0 });

      // The re-read starts once the PUT resolved, and nothing navigates
      // before it settles.
      await waitFor(() => expect(tasksApi.getTask).toHaveBeenCalledTimes(2));
      expect(vi.mocked(tasksApi.getTask).mock.calls[1][0]).toBe(5);
      expect(vi.mocked(tasksApi.updateTask).mock.invocationCallOrder[0]).toBeLessThan(
        vi.mocked(tasksApi.getTask).mock.invocationCallOrder[1]
      );
      expect(navigate).not.toHaveBeenCalled();

      // The API kept the link (the caller could not clear it).
      reRead.resolve(linkedTask({ title: 'Call the client (re-read)' }));

      await waitFor(() => expect(navigate).toHaveBeenCalledWith('/tasks/5'));
      expect(navigate).toHaveBeenCalledTimes(1);
      const cached = client.getQueryData<Task>(['task', '5']);
      expect(cached?.title).toBe('Call the client (re-read)');
      expect(cached?.deal).toEqual(linkedSummary);
      expect(cached?.deal_id).toBe(7);
    });

    it('still opens the task when the re-read fails', async () => {
      editMode();
      const client = createProductionQueryClient();
      vi.mocked(tasksApi.getTask)
        .mockResolvedValueOnce(linkedTask())
        .mockRejectedValueOnce(new Error('network down'));
      vi.mocked(tasksApi.updateTask).mockResolvedValue(
        linkedTask({ title: 'From the PUT body', deal_id: undefined, deal: undefined })
      );

      renderForm(client);
      await formReady();
      await waitFor(() =>
        expect(within(dealPicker()).getByLabelText('Deal')).toHaveValue('Website redesign')
      );

      fireEvent.click(within(dealPicker()).getByLabelText('Clear'));
      await waitFor(() => expect(within(dealPicker()).getByLabelText('Deal')).toHaveValue(''));
      fireEvent.click(screen.getByRole('button', { name: 'Update Task' }));

      await waitFor(() => expect(navigate).toHaveBeenCalledWith('/tasks/5'));
      expect(updatePayload()).toMatchObject({ deal_id: 0 });
      expect(tasksApi.getTask).toHaveBeenCalledTimes(2);
      expect(isInvalidated(client, ['task', '5'])).toBe(true);
      // Never filled from the PUT body.
      expect(client.getQueryData<Task>(['task', '5'])?.title).not.toBe('From the PUT body');
    });

    it('marks every cached deal task list stale after a create and after an update', async () => {
      const client = createProductionQueryClient();
      const dealSevenTasks = ['deal', 7, 'tasks', { page: 0, limit: 5 }];
      const dealNineTasks = ['deal', 9, 'tasks', { page: 1, limit: 10 }];
      const seed = () => {
        client.setQueryData(dealSevenTasks, { tasks: [], total: 0 });
        client.setQueryData(dealNineTasks, { tasks: [], total: 0 });
      };

      // A create.
      seed();
      expect(isInvalidated(client, dealSevenTasks)).toBe(false);
      expect(isInvalidated(client, dealNineTasks)).toBe(false);
      const created = renderForm(client);
      await formReady();
      fillTitle('Prepare the contract');
      fireEvent.click(screen.getByRole('button', { name: 'Create Task' }));
      await waitFor(() => expect(navigate).toHaveBeenCalledWith('/tasks'));
      expect(isInvalidated(client, dealSevenTasks)).toBe(true);
      expect(isInvalidated(client, dealNineTasks)).toBe(true);
      created.unmount();

      // An update.
      seed();
      expect(isInvalidated(client, dealSevenTasks)).toBe(false);
      expect(isInvalidated(client, dealNineTasks)).toBe(false);
      editMode();
      renderForm(client);
      await formReady();
      await waitFor(() => expect(screen.getByLabelText(/^Title/)).toHaveValue('Call the client'));
      fireEvent.click(screen.getByRole('button', { name: 'Update Task' }));
      await waitFor(() => expect(navigate).toHaveBeenCalledWith('/tasks/5'));
      expect(isInvalidated(client, dealSevenTasks)).toBe(true);
      expect(isInvalidated(client, dealNineTasks)).toBe(true);
    });
  });
});
