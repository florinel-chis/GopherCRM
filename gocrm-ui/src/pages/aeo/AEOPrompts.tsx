import React, { useEffect, useMemo, useState } from 'react';
import { useForm, FormProvider } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import * as z from 'zod';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import {
  Alert,
  Box,
  Button,
  Card,
  CardContent,
  Checkbox,
  Chip,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
  Divider,
  Drawer,
  FormControl,
  FormControlLabel,
  IconButton,
  InputLabel,
  LinearProgress,
  MenuItem,
  Paper,
  Select,
  Stack,
  Switch,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  ToggleButton,
  ToggleButtonGroup,
  Tooltip,
  Typography,
} from '@mui/material';
import {
  Add as AddIcon,
  AutoAwesome as AutoAwesomeIcon,
  Close as CloseIcon,
  Delete as DeleteIcon,
  Edit as EditIcon,
} from '@mui/icons-material';
import { AxiosError } from 'axios';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { FormTextField } from '@/components/form';
import { Loading } from '@/components/Loading';
import { useAuth } from '@/hooks/useAuth';
import { useSnackbar } from '@/hooks/useSnackbar';
import { aeoApi, type AEOAnswer, type AEOPrompt } from '@/api/endpoints/aeo';

const DAY_WINDOWS = [7, 30, 90] as const;
const MAX_PROMPTS_PER_BATCH = 25;
const MAX_PROMPT_LENGTH = 500;

// Word boundary as the backend defines it: the neighbouring rune must not be a
// letter, a digit or an underscore. Deliberately not \b, so "café" and "Ştefan"
// behave and "Acme" does not match inside "Acmerica".
const isWordRune = (rune: string | undefined): boolean =>
  rune !== undefined && /[\p{L}\p{N}_]/u.test(rune);

interface Segment {
  text: string;
  match: boolean;
}

// Splits `text` into plain and matched segments for every brand term. Used to
// mark the brand up inside an answer transcript; matching mirrors the Go
// detector so what is highlighted is what was counted.
export const segmentMentions = (text: string, terms: string[]): Segment[] => {
  const cleaned = terms.map((term) => term.trim()).filter((term) => term.length > 0);
  if (!text || cleaned.length === 0) {
    return [{ text, match: false }];
  }

  const haystack = text.toLowerCase();
  const hits: Array<{ start: number; end: number }> = [];

  for (const term of cleaned) {
    const needle = term.toLowerCase();
    let from = 0;
    for (;;) {
      const at = haystack.indexOf(needle, from);
      if (at === -1) {
        break;
      }
      const end = at + needle.length;
      if (!isWordRune(text[at - 1]) && !isWordRune(text[end])) {
        hits.push({ start: at, end });
      }
      from = at + 1;
    }
  }

  if (hits.length === 0) {
    return [{ text, match: false }];
  }

  hits.sort((a, b) => a.start - b.start || b.end - a.end);

  const segments: Segment[] = [];
  let cursor = 0;
  for (const hit of hits) {
    if (hit.start < cursor) {
      continue; // overlapping alias, the longer earlier match already covers it
    }
    if (hit.start > cursor) {
      segments.push({ text: text.slice(cursor, hit.start), match: false });
    }
    segments.push({ text: text.slice(hit.start, hit.end), match: true });
    cursor = hit.end;
  }
  if (cursor < text.length) {
    segments.push({ text: text.slice(cursor), match: false });
  }
  return segments;
};

const Transcript: React.FC<{ text: string; terms: string[] }> = ({ text, terms }) => (
  <Typography
    variant="body2"
    component="p"
    sx={{ whiteSpace: 'pre-wrap' }}
    data-testid="answer-transcript"
  >
    {segmentMentions(text, terms).map((segment, index) =>
      segment.match ? (
        <Box
          key={index}
          component="mark"
          data-testid="brand-mention"
          sx={{ backgroundColor: 'warning.light', px: 0.25, borderRadius: '2px' }}
        >
          {segment.text}
        </Box>
      ) : (
        <React.Fragment key={index}>{segment.text}</React.Fragment>
      )
    )}
  </Typography>
);

const formatDateTime = (value?: string): string =>
  value ? new Date(value).toLocaleString() : '—';

const addPromptsSchema = z.object({
  prompts: z
    .string()
    .trim()
    .min(1, 'Enter at least one prompt')
    .superRefine((value, ctx) => {
      const lines = value
        .split('\n')
        .map((line) => line.trim())
        .filter((line) => line.length > 0);
      if (lines.length === 0) {
        ctx.addIssue({ code: z.ZodIssueCode.custom, message: 'Enter at least one prompt' });
        return;
      }
      if (lines.length > MAX_PROMPTS_PER_BATCH) {
        ctx.addIssue({
          code: z.ZodIssueCode.custom,
          message: `At most ${MAX_PROMPTS_PER_BATCH} prompts per batch`,
        });
      }
      if (lines.some((line) => line.length > MAX_PROMPT_LENGTH)) {
        ctx.addIssue({
          code: z.ZodIssueCode.custom,
          message: `Each prompt must be ${MAX_PROMPT_LENGTH} characters or fewer`,
        });
      }
    }),
});

type AddPromptsFormData = z.infer<typeof addPromptsSchema>;

const editPromptSchema = z.object({
  text: z
    .string()
    .trim()
    .min(1, 'Prompt text is required')
    .max(MAX_PROMPT_LENGTH, `Prompt must be ${MAX_PROMPT_LENGTH} characters or fewer`),
});

type EditPromptFormData = z.infer<typeof editPromptSchema>;

export const Component: React.FC = () => {
  const queryClient = useQueryClient();
  const { user } = useAuth();
  const { showSuccess, showError } = useSnackbar();

  // Matches the API: admin and sales may add, edit and toggle prompts; only an
  // admin may delete. Support is read-only, customers never reach the route.
  const canManage = user?.role === 'admin' || user?.role === 'sales';
  const canDelete = user?.role === 'admin';

  const [days, setDays] = useState<number>(30);
  const [selected, setSelected] = useState<AEOPrompt | null>(null);
  const [runFilter, setRunFilter] = useState<number | 'all'>('all');
  const [addOpen, setAddOpen] = useState(false);
  const [generateOpen, setGenerateOpen] = useState(false);
  const [suggestions, setSuggestions] = useState<string[]>([]);
  const [chosen, setChosen] = useState<Record<string, boolean>>({});
  const [editing, setEditing] = useState<AEOPrompt | null>(null);
  const [deleteDialog, setDeleteDialog] = useState<{ open: boolean; prompt?: AEOPrompt }>({
    open: false,
  });

  const {
    data: prompts,
    isLoading,
    isError,
    error,
  } = useQuery({
    queryKey: ['aeo', 'prompts', days],
    queryFn: () => aeoApi.getPrompts({ days }),
  });

  // Brand terms drive the transcript highlighting; the profile may legitimately
  // be missing, in which case nothing is highlighted.
  const { data: profile } = useQuery({
    queryKey: ['aeo', 'profile'],
    queryFn: () => aeoApi.getProfile(),
  });

  const brandTerms = useMemo(
    () => (profile ? [profile.brand_name, ...(profile.brand_aliases || [])] : []),
    [profile]
  );

  const { data: runs } = useQuery({
    queryKey: ['aeo', 'runs'],
    queryFn: () => aeoApi.getRuns({ limit: 20 }),
    enabled: selected !== null,
  });

  const {
    data: answers,
    isLoading: answersLoading,
    isError: answersError,
  } = useQuery({
    queryKey: ['aeo', 'answers', selected?.id, runFilter],
    queryFn: () =>
      aeoApi.getPromptAnswers(selected!.id, {
        run_id: runFilter === 'all' ? undefined : runFilter,
        limit: 50,
      }),
    enabled: selected !== null,
  });

  const addMethods = useForm<AddPromptsFormData>({
    resolver: zodResolver(addPromptsSchema),
    defaultValues: { prompts: '' },
  });

  const editMethods = useForm<EditPromptFormData>({
    resolver: zodResolver(editPromptSchema),
    defaultValues: { text: '' },
  });

  useEffect(() => {
    if (editing) {
      editMethods.reset({ text: editing.text });
    }
  }, [editing, editMethods]);

  const invalidatePrompts = () => {
    queryClient.invalidateQueries({ queryKey: ['aeo', 'prompts'] });
  };

  // A duplicate text is a 409; the 100-active-prompt cap comes back as a 400
  // with the server's own wording, which is more useful than a generic message.
  const reportMutationError = (mutationError: unknown, fallback: string) => {
    if (mutationError instanceof AxiosError) {
      const message = (mutationError.response?.data as { message?: string } | undefined)?.message;
      if (mutationError.response?.status === 409) {
        showError(message || 'A prompt with this text already exists');
        return;
      }
      if (mutationError.response?.status === 400 && message) {
        showError(message);
        return;
      }
    }
    showError(fallback);
  };

  const createMutation = useMutation({
    mutationFn: (texts: string[]) => aeoApi.createPrompts(texts),
    onSuccess: (created) => {
      showSuccess(`${created.length} prompt${created.length === 1 ? '' : 's'} added`);
      invalidatePrompts();
      setAddOpen(false);
      setGenerateOpen(false);
      setSuggestions([]);
      setChosen({});
      addMethods.reset({ prompts: '' });
    },
    onError: (mutationError) => reportMutationError(mutationError, 'Failed to add prompts'),
  });

  const updateMutation = useMutation({
    mutationFn: ({ id, data }: { id: number; data: { text?: string; is_active?: boolean } }) =>
      aeoApi.updatePrompt(id, data),
    onSuccess: (updated) => {
      showSuccess('Prompt updated');
      invalidatePrompts();
      setEditing(null);
      setSelected((current) => (current && current.id === updated.id ? updated : current));
    },
    onError: (mutationError) => reportMutationError(mutationError, 'Failed to update the prompt'),
  });

  const deleteMutation = useMutation({
    mutationFn: (id: number) => aeoApi.deletePrompt(id),
    onSuccess: (_data, id) => {
      showSuccess('Prompt deleted');
      invalidatePrompts();
      setDeleteDialog({ open: false });
      setSelected((current) => (current && current.id === id ? null : current));
    },
    onError: () => showError('Failed to delete the prompt'),
  });

  const generateMutation = useMutation({
    mutationFn: (count: number) => aeoApi.generatePrompts(count),
    onSuccess: (generated) => {
      setSuggestions(generated);
      setChosen(Object.fromEntries(generated.map((text) => [text, true])));
      if (generated.length === 0) {
        showError('The generator returned no suggestions');
      }
    },
    onError: (mutationError) => {
      if (
        mutationError instanceof AxiosError &&
        mutationError.response?.status === 503
      ) {
        showError(
          (mutationError.response?.data as { message?: string } | undefined)?.message ||
            'Prompt generation is not configured on the server'
        );
        return;
      }
      showError('Failed to generate prompt suggestions');
    },
  });

  const openDrawer = (prompt: AEOPrompt) => {
    setSelected(prompt);
    setRunFilter('all');
  };

  const onAddSubmit = (data: AddPromptsFormData) => {
    const texts = data.prompts
      .split('\n')
      .map((line) => line.trim())
      .filter((line) => line.length > 0);
    createMutation.mutate(texts);
  };

  const onEditSubmit = (data: EditPromptFormData) => {
    if (editing) {
      updateMutation.mutate({ id: editing.id, data: { text: data.text.trim() } });
    }
  };

  if (isLoading) {
    return <Loading />;
  }

  if (isError) {
    return (
      <Alert severity="error">
        Failed to load AEO prompts
        {error instanceof Error ? `: ${error.message}` : ''}
      </Alert>
    );
  }

  const rows = prompts || [];
  const pendingDelete = deleteDialog.prompt;
  const chosenTexts = suggestions.filter((text) => chosen[text]);

  return (
    <Box>
      <Box display="flex" justifyContent="space-between" alignItems="center" mb={3} gap={2}>
        <Typography variant="h4">AEO Prompts</Typography>
        <Stack direction="row" spacing={1} alignItems="center">
          <ToggleButtonGroup
            size="small"
            exclusive
            value={days}
            onChange={(_event, value) => {
              if (value !== null) {
                setDays(value);
              }
            }}
            aria-label="Reporting window"
          >
            {DAY_WINDOWS.map((window) => (
              <ToggleButton key={window} value={window} aria-label={`${window} days`}>
                {window}d
              </ToggleButton>
            ))}
          </ToggleButtonGroup>
          {canManage && (
            <>
              <Button
                variant="outlined"
                startIcon={<AutoAwesomeIcon />}
                onClick={() => {
                  setSuggestions([]);
                  setChosen({});
                  setGenerateOpen(true);
                }}
              >
                Generate with AI
              </Button>
              <Button variant="contained" startIcon={<AddIcon />} onClick={() => setAddOpen(true)}>
                Add prompts
              </Button>
            </>
          )}
        </Stack>
      </Box>

      <Paper>
        <TableContainer>
          <Table>
            <TableHead>
              <TableRow>
                <TableCell>Prompt</TableCell>
                <TableCell align="right">Visibility</TableCell>
                <TableCell align="right">Answers</TableCell>
                <TableCell align="right">Mentions</TableCell>
                <TableCell>Last run</TableCell>
                <TableCell>Active</TableCell>
                <TableCell align="right">Actions</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {rows.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={7}>
                    <Typography variant="body2" color="text.secondary">
                      No AEO data yet — add a prompt to start tracking answers.
                    </Typography>
                  </TableCell>
                </TableRow>
              ) : (
                rows.map((prompt) => (
                  <TableRow key={prompt.id} hover>
                    <TableCell>
                      <Button
                        variant="text"
                        sx={{ textAlign: 'left', textTransform: 'none' }}
                        onClick={() => openDrawer(prompt)}
                      >
                        {prompt.text}
                      </Button>
                    </TableCell>
                    <TableCell align="right">
                      <Box display="flex" alignItems="center" gap={1} justifyContent="flex-end">
                        <Box sx={{ width: 60 }}>
                          <LinearProgress
                            variant="determinate"
                            value={Math.min(100, Math.max(0, prompt.visibility))}
                            data-testid={`visibility-bar-${prompt.id}`}
                          />
                        </Box>
                        <Typography variant="body2">{prompt.visibility.toFixed(1)}%</Typography>
                      </Box>
                    </TableCell>
                    <TableCell align="right">{prompt.answer_count}</TableCell>
                    <TableCell align="right">{prompt.mention_count}</TableCell>
                    <TableCell>{formatDateTime(prompt.last_run_at)}</TableCell>
                    <TableCell>
                      {canManage ? (
                        <Switch
                          size="small"
                          checked={prompt.is_active}
                          slotProps={{ input: { 'aria-label': `Toggle ${prompt.text}` } }}
                          onChange={(event) =>
                            updateMutation.mutate({
                              id: prompt.id,
                              data: { is_active: event.target.checked },
                            })
                          }
                        />
                      ) : (
                        <Chip
                          size="small"
                          label={prompt.is_active ? 'Active' : 'Paused'}
                          color={prompt.is_active ? 'success' : 'default'}
                        />
                      )}
                    </TableCell>
                    <TableCell align="right">
                      <Box display="flex" gap={1} justifyContent="flex-end">
                        {canManage && (
                          <Tooltip title="Edit prompt">
                            <IconButton size="small" onClick={() => setEditing(prompt)}>
                              <EditIcon fontSize="small" />
                            </IconButton>
                          </Tooltip>
                        )}
                        {canDelete && (
                          <Tooltip title="Delete prompt">
                            <IconButton
                              size="small"
                              onClick={() => setDeleteDialog({ open: true, prompt })}
                            >
                              <DeleteIcon fontSize="small" />
                            </IconButton>
                          </Tooltip>
                        )}
                      </Box>
                    </TableCell>
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
        </TableContainer>
      </Paper>

      <Drawer
        anchor="right"
        open={selected !== null}
        onClose={() => setSelected(null)}
        slotProps={{ paper: { sx: { width: { xs: '100%', md: 640 }, p: 3 } } }}
      >
        {selected && (
          <Box>
            <Box display="flex" justifyContent="space-between" alignItems="flex-start" mb={2}>
              <Typography variant="h6">{selected.text}</Typography>
              <IconButton aria-label="Close prompt details" onClick={() => setSelected(null)}>
                <CloseIcon />
              </IconButton>
            </Box>

            <Stack direction="row" spacing={1} mb={2} flexWrap="wrap">
              <Chip label={`Visibility ${selected.visibility.toFixed(1)}%`} color="primary" />
              <Chip label={`${selected.answer_count} answers`} variant="outlined" />
              <Chip label={`${selected.mention_count} mentions`} variant="outlined" />
            </Stack>

            <FormControl size="small" fullWidth sx={{ mb: 2 }}>
              <InputLabel id="aeo-run-filter-label">Run</InputLabel>
              <Select
                labelId="aeo-run-filter-label"
                label="Run"
                value={runFilter}
                onChange={(event) =>
                  setRunFilter(
                    event.target.value === 'all' ? 'all' : Number(event.target.value)
                  )
                }
              >
                <MenuItem value="all">All runs</MenuItem>
                {(runs || []).map((run) => (
                  <MenuItem key={run.id} value={run.id}>
                    #{run.id} · {formatDateTime(run.started_at)} · {run.status}
                  </MenuItem>
                ))}
              </Select>
            </FormControl>

            <Divider sx={{ mb: 2 }} />

            {answersLoading ? (
              <Box display="flex" justifyContent="center" py={4}>
                <CircularProgress />
              </Box>
            ) : answersError ? (
              <Alert severity="error">Failed to load answers for this prompt</Alert>
            ) : (answers || []).length === 0 ? (
              <Typography variant="body2" color="text.secondary">
                No AEO data yet — this prompt has not been answered in the selected run.
              </Typography>
            ) : (
              <Stack spacing={2}>
                {(answers as AEOAnswer[]).map((answer) => (
                  <Card key={answer.id} variant="outlined" data-testid={`answer-card-${answer.id}`}>
                    <CardContent>
                      <Box display="flex" justifyContent="space-between" alignItems="center" mb={1}>
                        <Typography variant="subtitle1">{answer.provider}</Typography>
                        <Chip
                          size="small"
                          label={answer.brand_mentioned ? 'Mentioned' : 'No mentions'}
                          color={answer.brand_mentioned ? 'success' : 'default'}
                        />
                      </Box>
                      <Typography variant="caption" color="text.secondary">
                        {answer.model} · {answer.latency_ms} ms · {formatDateTime(answer.created_at)}
                      </Typography>

                      {answer.error ? (
                        <Alert severity="error" sx={{ mt: 2 }}>
                          {answer.error}
                        </Alert>
                      ) : (
                        <Box mt={2}>
                          <Transcript text={answer.answer_text} terms={brandTerms} />
                        </Box>
                      )}

                      {Object.keys(answer.competitor_mentions || {}).length > 0 && (
                        <Box mt={2} display="flex" gap={1} flexWrap="wrap">
                          {Object.entries(answer.competitor_mentions).map(([name, count]) => (
                            <Chip key={name} size="small" label={`${name} × ${count}`} />
                          ))}
                        </Box>
                      )}

                      {(answer.citations || []).length > 0 && (
                        <Box mt={2}>
                          <Typography variant="subtitle2" gutterBottom>
                            Citations
                          </Typography>
                          <Stack spacing={0.5}>
                            {answer.citations.map((citation) => (
                              <Typography key={citation.id} variant="body2">
                                {citation.domain}
                                {citation.is_owned ? ' (owned)' : ''}
                                {citation.competitor_name ? ` (${citation.competitor_name})` : ''}
                              </Typography>
                            ))}
                          </Stack>
                        </Box>
                      )}
                    </CardContent>
                  </Card>
                ))}
              </Stack>
            )}
          </Box>
        )}
      </Drawer>

      <Dialog open={addOpen} onClose={() => setAddOpen(false)} maxWidth="sm" fullWidth>
        <FormProvider {...addMethods}>
          <form onSubmit={addMethods.handleSubmit(onAddSubmit)}>
            <DialogTitle>Add prompts</DialogTitle>
            <DialogContent>
              <DialogContentText sx={{ mb: 2 }}>
                One prompt per line, up to {MAX_PROMPTS_PER_BATCH} at a time. The batch is saved
                all-or-nothing.
              </DialogContentText>
              <FormTextField
                name="prompts"
                label="Prompts"
                multiline
                minRows={5}
                autoFocus
              />
            </DialogContent>
            <DialogActions>
              <Button color="inherit" onClick={() => setAddOpen(false)}>
                Cancel
              </Button>
              <Button type="submit" variant="contained" disabled={createMutation.isPending}>
                Add
              </Button>
            </DialogActions>
          </form>
        </FormProvider>
      </Dialog>

      <Dialog open={generateOpen} onClose={() => setGenerateOpen(false)} maxWidth="sm" fullWidth>
        <DialogTitle>Generate prompts</DialogTitle>
        <DialogContent>
          <DialogContentText sx={{ mb: 2 }}>
            Suggestions are drafted from the brand profile. Nothing is saved until you add the
            ones you want.
          </DialogContentText>
          <Button
            variant="outlined"
            startIcon={<AutoAwesomeIcon />}
            onClick={() => generateMutation.mutate(10)}
            disabled={generateMutation.isPending}
          >
            {generateMutation.isPending ? 'Generating…' : 'Generate suggestions'}
          </Button>
          <Box mt={2}>
            {suggestions.length === 0 ? (
              <Typography variant="body2" color="text.secondary">
                No suggestions yet.
              </Typography>
            ) : (
              <Stack>
                {suggestions.map((text) => (
                  <FormControlLabel
                    key={text}
                    control={
                      <Checkbox
                        checked={Boolean(chosen[text])}
                        onChange={(event) =>
                          setChosen((current) => ({ ...current, [text]: event.target.checked }))
                        }
                      />
                    }
                    label={text}
                  />
                ))}
              </Stack>
            )}
          </Box>
        </DialogContent>
        <DialogActions>
          <Button color="inherit" onClick={() => setGenerateOpen(false)}>
            Cancel
          </Button>
          <Button
            variant="contained"
            disabled={chosenTexts.length === 0 || createMutation.isPending}
            onClick={() => createMutation.mutate(chosenTexts)}
          >
            Add selected
          </Button>
        </DialogActions>
      </Dialog>

      <Dialog open={editing !== null} onClose={() => setEditing(null)} maxWidth="sm" fullWidth>
        <FormProvider {...editMethods}>
          <form onSubmit={editMethods.handleSubmit(onEditSubmit)}>
            <DialogTitle>Edit prompt</DialogTitle>
            <DialogContent>
              <Box mt={1}>
                <FormTextField name="text" label="Prompt text" multiline minRows={3} autoFocus />
              </Box>
            </DialogContent>
            <DialogActions>
              <Button color="inherit" onClick={() => setEditing(null)}>
                Cancel
              </Button>
              <Button type="submit" variant="contained" disabled={updateMutation.isPending}>
                Save
              </Button>
            </DialogActions>
          </form>
        </FormProvider>
      </Dialog>

      <ConfirmDialog
        open={deleteDialog.open}
        title="Delete prompt"
        message={
          pendingDelete
            ? `Delete the prompt "${pendingDelete.text}"? Answers already collected for it stay in the run history.`
            : ''
        }
        severity="error"
        confirmText="Delete"
        onConfirm={() => {
          if (pendingDelete) {
            deleteMutation.mutate(pendingDelete.id);
          }
        }}
        onCancel={() => setDeleteDialog({ open: false })}
      />
    </Box>
  );
};

Component.displayName = 'AEOPrompts';
