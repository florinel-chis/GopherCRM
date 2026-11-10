import React, { useState } from 'react';
import { Link as RouterLink } from 'react-router-dom';
import { keepPreviousData, useQuery } from '@tanstack/react-query';
import {
  Box,
  Chip,
  Link,
  Paper,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TablePagination,
  TableRow,
  Typography,
} from '@mui/material';
import { tasksApi } from '@/api/endpoints';
import { useAuth } from '@/hooks/useAuth';
import type { Deal, Task } from '@/types';
import { formatDate } from '@/utils/date';

const ROWS_PER_PAGE_OPTIONS = [5, 10, 25];

const STATUS_LABELS: Record<Task['status'], string> = {
  pending: 'Pending',
  in_progress: 'In Progress',
  completed: 'Completed',
  cancelled: 'Cancelled',
};

const PRIORITY_LABELS: Record<Task['priority'], string> = {
  low: 'Low',
  medium: 'Medium',
  high: 'High',
};

export interface DealTasksSectionProps {
  deal: Pick<Deal, 'id'>;
}

/**
 * The "Tasks" section of a deal page: one small paginated table over
 * GET /deals/:id/tasks. Read-only; tasks are linked to a deal from the task
 * form. The query key ['deal', id, 'tasks', paging] is what a task save marks
 * stale.
 */
export const DealTasksSection: React.FC<DealTasksSectionProps> = ({ deal }) => {
  const { user } = useAuth();
  // The API gives sales users only the tasks assigned to them (and counts only
  // those in the total); say so, so a short list is not read as complete.
  const assignedOnly = user?.role === 'sales';
  const [paging, setPaging] = useState({ page: 0, limit: 5 });

  const { data, isError } = useQuery({
    queryKey: ['deal', deal.id, 'tasks', paging],
    queryFn: () =>
      tasksApi.getDealTasks(deal.id, { offset: paging.page * paging.limit, limit: paging.limit }),
    // Keep the current page on screen while the next one loads, so the
    // pagination control stays put.
    placeholderData: keepPreviousData,
  });

  return (
    <Paper sx={{ p: 3, mb: 3 }} component="section" aria-labelledby="deal-tasks-heading">
      <Box mb={2}>
        <Typography variant="h6" id="deal-tasks-heading">
          Tasks{data && ` (${data.total})`}
        </Typography>
        {assignedOnly && (
          <Typography variant="caption" color="text.secondary">
            Only tasks assigned to you are shown
          </Typography>
        )}
      </Box>
      {data && data.tasks.length > 0 ? (
        <>
          <Table size="small" aria-label="Tasks">
            <TableHead>
              <TableRow>
                <TableCell>Title</TableCell>
                <TableCell>Status</TableCell>
                <TableCell>Priority</TableCell>
                <TableCell>Due date</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {data.tasks.map((task) => (
                <TableRow key={task.id} hover data-testid="deal-task-row">
                  <TableCell>
                    <Link component={RouterLink} to={`/tasks/${task.id}`}>
                      {task.title}
                    </Link>
                  </TableCell>
                  <TableCell>
                    <Chip
                      size="small"
                      label={STATUS_LABELS[task.status] ?? task.status}
                      data-testid="deal-task-status"
                    />
                  </TableCell>
                  <TableCell>{PRIORITY_LABELS[task.priority] ?? task.priority}</TableCell>
                  <TableCell>
                    {task.due_date ? formatDate(task.due_date, 'MMM dd, yyyy') : '—'}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
          <TablePagination
            component="div"
            count={data.total}
            page={paging.page}
            rowsPerPage={paging.limit}
            rowsPerPageOptions={ROWS_PER_PAGE_OPTIONS}
            onPageChange={(_, page) => setPaging((prev) => ({ ...prev, page }))}
            onRowsPerPageChange={(event) =>
              setPaging({ page: 0, limit: parseInt(event.target.value, 10) })
            }
          />
        </>
      ) : data ? (
        <Typography color="text.secondary">No tasks for this deal</Typography>
      ) : isError ? (
        <Typography color="error">The tasks could not be loaded</Typography>
      ) : null}
    </Paper>
  );
};
