import React, { useState, useEffect } from 'react';
import {
  Box,
  Paper,
  Typography,
  Tabs,
  Tab,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  IconButton,
  Chip,
  TextField,
  Switch,
  FormControlLabel,
  Button,
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  MenuItem,
  Select,
  FormControl,
  InputLabel,
  Alert,
} from '@mui/material';
import {
  Edit as EditIcon,
  Refresh as RefreshIcon,
  Save as SaveIcon,
  Cancel as CancelIcon,
} from '@mui/icons-material';
import type { Configuration } from '@/api/endpoints/configurations';
import { configurationsApi } from '@/api/endpoints/configurations';
import { useSnackbar } from '@/hooks/useSnackbar';
import { Loading } from '@/components/Loading';

interface TabPanelProps {
  children?: React.ReactNode;
  index: number;
  value: number;
}

function TabPanel(props: TabPanelProps) {
  const { children, value, index, ...other } = props;

  return (
    <div
      role="tabpanel"
      hidden={value !== index}
      id={`configuration-tabpanel-${index}`}
      aria-labelledby={`configuration-tab-${index}`}
      {...other}
    >
      {value === index && <Box sx={{ p: 3 }}>{children}</Box>}
    </div>
  );
}

const CATEGORIES = [
  { value: 'general', label: 'General' },
  { value: 'ui', label: 'UI & Theme' },
  { value: 'security', label: 'Security' },
  { value: 'leads', label: 'Leads' },
  { value: 'customers', label: 'Customers' },
  { value: 'tickets', label: 'Tickets' },
  { value: 'tasks', label: 'Tasks' },
  { value: 'integration', label: 'Integration' },
];

const ConfigurationSettings: React.FC = () => {
  const [configurations, setConfigurations] = useState<Configuration[]>([]);
  const [loading, setLoading] = useState(true);
  const [activeTab, setActiveTab] = useState(0);
  const [editingConfig, setEditingConfig] = useState<Configuration | null>(null);
  const [editValue, setEditValue] = useState<any>('');
  const [showEditDialog, setShowEditDialog] = useState(false);
  const { showSnackbar } = useSnackbar();

  useEffect(() => {
    loadConfigurations();
  }, []);

  const loadConfigurations = async () => {
    try {
      setLoading(true);
      const configs = await configurationsApi.getAll();
      setConfigurations(configs);
    } catch (error) {
      showSnackbar('Failed to load configurations', 'error');
    } finally {
      setLoading(false);
    }
  };

  const handleTabChange = (event: React.SyntheticEvent, newValue: number) => {
    setActiveTab(newValue);
  };

  const handleEdit = (config: Configuration) => {
    setEditingConfig(config);
    setEditValue(configurationsApi.getValue(config));
    setShowEditDialog(true);
  };

  const handleSaveEdit = async () => {
    if (!editingConfig) return;

    try {
      await configurationsApi.set(editingConfig.key, { value: editValue });
      showSnackbar('Configuration updated successfully', 'success');
      await loadConfigurations();
      setShowEditDialog(false);
      setEditingConfig(null);
    } catch (error) {
      showSnackbar('Failed to update configuration', 'error');
    }
  };

  const handleReset = async (config: Configuration) => {
    try {
      await configurationsApi.reset(config.key);
      showSnackbar('Configuration reset to default', 'success');
      await loadConfigurations();
    } catch (error) {
      showSnackbar('Failed to reset configuration', 'error');
    }
  };

  const getConfigurationsByCategory = (category: string) => {
    return configurations.filter(config => config.category === category);
  };

  const renderConfigValue = (config: Configuration) => {
    const value = configurationsApi.getValue(config);
    
    switch (config.type) {
      case 'boolean':
        return (
          <Chip 
            label={value ? 'True' : 'False'} 
            color={value ? 'success' : 'default'}
            size="small"
          />
        );
      case 'array':
        return (
          <Box>
            {Array.isArray(value) ? value.map((item, index) => (
              <Chip key={index} label={String(item)} size="small" sx={{ mr: 0.5, mb: 0.5 }} />
            )) : 'Invalid array'}
          </Box>
        );
      case 'json':
        return (
          <Typography variant="body2" sx={{ fontFamily: 'monospace', fontSize: '0.75rem' }}>
            {JSON.stringify(value, null, 2)}
          </Typography>
        );
      default:
        return <Typography variant="body2">{String(value)}</Typography>;
    }
  };

  const renderEditField = () => {
    if (!editingConfig) return null;

    const validValues = editingConfig.valid_values ? 
      JSON.parse(editingConfig.valid_values) : null;

    switch (editingConfig.type) {
      case 'boolean':
        return (
          <FormControlLabel
            control={
              <Switch
                checked={editValue === true}
                onChange={(e) => setEditValue(e.target.checked)}
              />
            }
            label={editValue ? 'True' : 'False'}
          />
        );
      
      case 'integer':
        return (
          <TextField
            fullWidth
            type="number"
            value={editValue}
            onChange={(e) => setEditValue(parseInt(e.target.value, 10))}
            inputProps={{ step: 1 }}
          />
        );
      
      case 'float':
        return (
          <TextField
            fullWidth
            type="number"
            value={editValue}
            onChange={(e) => setEditValue(parseFloat(e.target.value))}
            inputProps={{ step: 0.1 }}
          />
        );
      
      case 'array':
        if (validValues && Array.isArray(validValues)) {
          return (
            <FormControl fullWidth>
              <InputLabel>Select Values</InputLabel>
              <Select
                multiple
                value={Array.isArray(editValue) ? editValue : []}
                onChange={(e) => setEditValue(e.target.value)}
                renderValue={(selected) => (
                  <Box sx={{ display: 'flex', flexWrap: 'wrap', gap: 0.5 }}>
                    {(selected as string[]).map((value) => (
                      <Chip key={value} label={value} size="small" />
                    ))}
                  </Box>
                )}
              >
                {validValues.map((option) => (
                  <MenuItem key={option} value={option}>
                    {option}
                  </MenuItem>
                ))}
              </Select>
            </FormControl>
          );
        } else {
          return (
            <TextField
              fullWidth
              multiline
              rows={3}
              value={Array.isArray(editValue) ? JSON.stringify(editValue, null, 2) : ''}
              onChange={(e) => {
                try {
                  setEditValue(JSON.parse(e.target.value));
                } catch {
                  // Keep the string value for editing
                }
              }}
              placeholder="Enter JSON array"
            />
          );
        }
      
      case 'json':
        return (
          <TextField
            fullWidth
            multiline
            rows={4}
            value={typeof editValue === 'object' ? JSON.stringify(editValue, null, 2) : editValue}
            onChange={(e) => {
              try {
                setEditValue(JSON.parse(e.target.value));
              } catch {
                // Keep the string value for editing
              }
            }}
            placeholder="Enter JSON"
          />
        );
      
      default:
        if (validValues && Array.isArray(validValues)) {
          return (
            <FormControl fullWidth>
              <InputLabel>Select Value</InputLabel>
              <Select
                value={editValue}
                onChange={(e) => setEditValue(e.target.value)}
              >
                {validValues.map((option) => (
                  <MenuItem key={option} value={option}>
                    {option}
                  </MenuItem>
                ))}
              </Select>
            </FormControl>
          );
        } else {
          return (
            <TextField
              fullWidth
              value={editValue}
              onChange={(e) => setEditValue(e.target.value)}
            />
          );
        }
    }
  };

  const renderConfigurationsTable = (categoryConfigs: Configuration[]) => (
    <TableContainer component={Paper}>
      <Table>
        <TableHead>
          <TableRow>
            <TableCell>Key</TableCell>
            <TableCell>Description</TableCell>
            <TableCell>Type</TableCell>
            <TableCell>Current Value</TableCell>
            <TableCell>Default Value</TableCell>
            <TableCell>System</TableCell>
            <TableCell>Read-Only</TableCell>
            <TableCell align="right">Actions</TableCell>
          </TableRow>
        </TableHead>
        <TableBody>
          {categoryConfigs.map((config) => (
            <TableRow key={config.key}>
              <TableCell>
                <Typography variant="body2" sx={{ fontFamily: 'monospace' }}>
                  {config.key}
                </Typography>
              </TableCell>
              <TableCell>
                <Typography variant="body2">{config.description}</Typography>
              </TableCell>
              <TableCell>
                <Chip label={config.type} size="small" variant="outlined" />
              </TableCell>
              <TableCell>{renderConfigValue(config)}</TableCell>
              <TableCell>
                <Typography variant="body2" color="text.secondary">
                  {config.default_value}
                </Typography>
              </TableCell>
              <TableCell>
                {config.is_system && <Chip label="System" size="small" color="info" />}
              </TableCell>
              <TableCell>
                {config.is_read_only && <Chip label="Read-Only" size="small" color="warning" />}
              </TableCell>
              <TableCell align="right">
                <IconButton
                  size="small"
                  onClick={() => handleEdit(config)}
                  disabled={config.is_read_only}
                  title="Edit configuration"
                >
                  <EditIcon />
                </IconButton>
                <IconButton
                  size="small"
                  onClick={() => handleReset(config)}
                  disabled={config.is_read_only}
                  title="Reset to default"
                >
                  <RefreshIcon />
                </IconButton>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </TableContainer>
  );

  if (loading) {
    return <Loading />;
  }

  return (
    <Box>
      <Typography variant="h4" gutterBottom>
        Configuration Settings
      </Typography>
      
      <Alert severity="warning" sx={{ mb: 3 }}>
        <strong>Warning:</strong> Modifying system configurations can affect application behavior. 
        Only change values if you understand their impact.
      </Alert>

      <Paper sx={{ width: '100%' }}>
        <Tabs value={activeTab} onChange={handleTabChange} variant="scrollable" scrollButtons="auto">
          {CATEGORIES.map((category, index) => (
            <Tab 
              key={category.value} 
              label={`${category.label} (${getConfigurationsByCategory(category.value).length})`}
            />
          ))}
        </Tabs>

        {CATEGORIES.map((category, index) => (
          <TabPanel key={category.value} value={activeTab} index={index}>
            {getConfigurationsByCategory(category.value).length > 0 ? (
              renderConfigurationsTable(getConfigurationsByCategory(category.value))
            ) : (
              <Typography color="text.secondary" sx={{ textAlign: 'center', py: 4 }}>
                No configurations found for {category.label}
              </Typography>
            )}
          </TabPanel>
        ))}
      </Paper>

      {/* Edit Dialog */}
      <Dialog 
        open={showEditDialog} 
        onClose={() => setShowEditDialog(false)}
        maxWidth="sm"
        fullWidth
      >
        <DialogTitle>
          Edit Configuration
          {editingConfig && (
            <Typography variant="body2" color="text.secondary" sx={{ mt: 1 }}>
              {editingConfig.key}
            </Typography>
          )}
        </DialogTitle>
        <DialogContent>
          {editingConfig && (
            <Box sx={{ mt: 2 }}>
              <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
                {editingConfig.description}
              </Typography>
              
              <Typography variant="body2" sx={{ mb: 1 }}>
                Type: <Chip label={editingConfig.type} size="small" variant="outlined" />
              </Typography>
              
              {editingConfig.valid_values && (
                <Typography variant="body2" sx={{ mb: 2 }}>
                  Valid values: {editingConfig.valid_values}
                </Typography>
              )}
              
              {renderEditField()}
            </Box>
          )}
        </DialogContent>
        <DialogActions>
          <Button 
            onClick={() => setShowEditDialog(false)}
            startIcon={<CancelIcon />}
          >
            Cancel
          </Button>
          <Button 
            onClick={handleSaveEdit}
            startIcon={<SaveIcon />}
            variant="contained"
          >
            Save
          </Button>
        </DialogActions>
      </Dialog>
    </Box>
  );
};

export function Component() {
  return <ConfigurationSettings />;
}

Component.displayName = 'ConfigurationSettings';