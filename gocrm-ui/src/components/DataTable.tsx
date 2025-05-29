import React, { useState } from 'react';
import {
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  TablePagination,
  TableSortLabel,
  Paper,
  Checkbox,
  IconButton,
  Toolbar,
  Typography,
  Box,
  TextField,
  InputAdornment,
  Tooltip,
  alpha,
  Skeleton,
} from '@mui/material';
import {
  Delete,
  Edit,
  Search,
  Visibility,
} from '@mui/icons-material';
import { visuallyHidden } from '@mui/utils';

export interface Column<T> {
  id: keyof T | string;
  label: string;
  minWidth?: number;
  align?: 'left' | 'right' | 'center';
  format?: (value: any, row: T) => React.ReactNode;
  sortable?: boolean;
  filterable?: boolean;
}

export interface DataTableProps<T> {
  columns: Column<T>[];
  data: T[];
  totalCount?: number;
  page?: number;
  rowsPerPage?: number;
  loading?: boolean;
  selectable?: boolean;
  selected?: (string | number)[];
  onSelectionChange?: (selected: (string | number)[]) => void;
  onPageChange?: (page: number) => void;
  onRowsPerPageChange?: (rowsPerPage: number) => void;
  onSort?: (field: string, order: 'asc' | 'desc') => void;
  onSearch?: (search: string) => void;
  onRowClick?: (row: T) => void;
  onEdit?: (row: T) => void;
  onDelete?: (row: T) => void;
  getRowId?: (row: T) => string | number;
  title?: string;
  actions?: React.ReactNode;
}

type Order = 'asc' | 'desc';


function DataTableHead<T>({
  columns,
  order,
  orderBy,
  onSelectAllClick,
  numSelected,
  rowCount,
  onRequestSort,
  selectable,
}: {
  columns: Column<T>[];
  order: Order;
  orderBy: string;
  onSelectAllClick: (event: React.ChangeEvent<HTMLInputElement>) => void;
  numSelected: number;
  rowCount: number;
  onRequestSort: (property: string) => void;
  selectable?: boolean;
}) {
  const createSortHandler = (property: string) => () => {
    onRequestSort(property);
  };

  return (
    <TableHead>
      <TableRow>
        {selectable && (
          <TableCell padding="checkbox">
            <Checkbox
              color="primary"
              indeterminate={numSelected > 0 && numSelected < rowCount}
              checked={rowCount > 0 && numSelected === rowCount}
              onChange={onSelectAllClick}
            />
          </TableCell>
        )}
        {columns.map((column) => (
          <TableCell
            key={column.id as string}
            align={column.align || 'left'}
            style={{ minWidth: column.minWidth }}
            sortDirection={orderBy === column.id ? order : false}
          >
            {column.sortable !== false ? (
              <TableSortLabel
                active={orderBy === column.id}
                direction={orderBy === column.id ? order : 'asc'}
                onClick={createSortHandler(column.id as string)}
              >
                {column.label}
                {orderBy === column.id ? (
                  <Box component="span" sx={visuallyHidden}>
                    {order === 'desc' ? 'sorted descending' : 'sorted ascending'}
                  </Box>
                ) : null}
              </TableSortLabel>
            ) : (
              column.label
            )}
          </TableCell>
        ))}
        <TableCell align="right">Actions</TableCell>
      </TableRow>
    </TableHead>
  );
}

function DataTableToolbar({
  numSelected,
  title,
  onSearch,
  actions,
  onDelete,
}: {
  numSelected: number;
  title?: string;
  onSearch?: (search: string) => void;
  actions?: React.ReactNode;
  onDelete?: () => void;
}) {
  const [searchValue, setSearchValue] = useState('');

  const handleSearchChange = (event: React.ChangeEvent<HTMLInputElement>) => {
    setSearchValue(event.target.value);
    onSearch?.(event.target.value);
  };

  return (
    <Toolbar
      sx={{
        pl: { sm: 2 },
        pr: { xs: 1, sm: 1 },
        ...(numSelected > 0 && {
          bgcolor: (theme) =>
            alpha(theme.palette.primary.main, theme.palette.action.activatedOpacity),
        }),
      }}
    >
      {numSelected > 0 ? (
        <Typography
          sx={{ flex: '1 1 100%' }}
          color="inherit"
          variant="subtitle1"
          component="div"
        >
          {numSelected} selected
        </Typography>
      ) : (
        <Typography
          sx={{ flex: '1 1 100%' }}
          variant="h6"
          id="tableTitle"
          component="div"
        >
          {title}
        </Typography>
      )}

      {numSelected > 0 ? (
        <Tooltip title="Delete">
          <IconButton onClick={onDelete}>
            <Delete />
          </IconButton>
        </Tooltip>
      ) : (
        <Box sx={{ display: 'flex', gap: 2, alignItems: 'center' }}>
          {onSearch && (
            <TextField
              variant="outlined"
              size="small"
              placeholder="Search..."
              value={searchValue}
              onChange={handleSearchChange}
              InputProps={{
                startAdornment: (
                  <InputAdornment position="start">
                    <Search />
                  </InputAdornment>
                ),
              }}
            />
          )}
          {actions}
        </Box>
      )}
    </Toolbar>
  );
}

export function DataTable<T extends { id?: string | number }>({
  columns,
  data,
  totalCount,
  page = 0,
  rowsPerPage = 10,
  loading = false,
  selectable = false,
  selected = [],
  onSelectionChange,
  onPageChange,
  onRowsPerPageChange,
  onSort,
  onSearch,
  onRowClick,
  onEdit,
  onDelete,
  getRowId = (row) => row.id as string | number,
  title,
  actions,
}: DataTableProps<T>) {
  const [order, setOrder] = useState<Order>('asc');
  const [orderBy, setOrderBy] = useState<string>('');
  const [internalSelected, setInternalSelected] = useState<(string | number)[]>(selected);

  const handleRequestSort = (property: string) => {
    const isAsc = orderBy === property && order === 'asc';
    const newOrder = isAsc ? 'desc' : 'asc';
    setOrder(newOrder);
    setOrderBy(property);
    onSort?.(property, newOrder);
  };

  const handleSelectAllClick = (event: React.ChangeEvent<HTMLInputElement>) => {
    if (event.target.checked) {
      const newSelected = data.map((row) => getRowId(row));
      setInternalSelected(newSelected);
      onSelectionChange?.(newSelected);
      return;
    }
    setInternalSelected([]);
    onSelectionChange?.([]);
  };

  const handleClick = (row: T) => {
    const id = getRowId(row);
    const selectedIndex = internalSelected.indexOf(id);
    let newSelected: (string | number)[] = [];

    if (selectedIndex === -1) {
      newSelected = newSelected.concat(internalSelected, id);
    } else if (selectedIndex === 0) {
      newSelected = newSelected.concat(internalSelected.slice(1));
    } else if (selectedIndex === internalSelected.length - 1) {
      newSelected = newSelected.concat(internalSelected.slice(0, -1));
    } else if (selectedIndex > 0) {
      newSelected = newSelected.concat(
        internalSelected.slice(0, selectedIndex),
        internalSelected.slice(selectedIndex + 1),
      );
    }

    setInternalSelected(newSelected);
    onSelectionChange?.(newSelected);
  };

  const handleChangePage = (_: unknown, newPage: number) => {
    onPageChange?.(newPage);
  };

  const handleChangeRowsPerPage = (event: React.ChangeEvent<HTMLInputElement>) => {
    onRowsPerPageChange?.(parseInt(event.target.value, 10));
  };

  const isSelected = (id: string | number) => internalSelected.indexOf(id) !== -1;

  const handleDeleteSelected = () => {
    // Handle bulk delete
    console.log('Delete selected:', internalSelected);
  };

  return (
    <Paper sx={{ width: '100%', overflow: 'hidden' }}>
      <DataTableToolbar
        numSelected={internalSelected.length}
        title={title}
        onSearch={onSearch}
        actions={actions}
        onDelete={handleDeleteSelected}
      />
      <TableContainer>
        <Table stickyHeader aria-labelledby="tableTitle">
          <DataTableHead
            columns={columns}
            order={order}
            orderBy={orderBy}
            onSelectAllClick={handleSelectAllClick}
            numSelected={internalSelected.length}
            rowCount={data.length}
            onRequestSort={handleRequestSort}
            selectable={selectable}
          />
          <TableBody>
            {loading ? (
              Array.from({ length: rowsPerPage }).map((_, index) => (
                <TableRow key={index}>
                  {selectable && (
                    <TableCell padding="checkbox">
                      <Skeleton variant="rectangular" width={20} height={20} />
                    </TableCell>
                  )}
                  {columns.map((column) => (
                    <TableCell key={column.id as string}>
                      <Skeleton variant="text" />
                    </TableCell>
                  ))}
                  <TableCell>
                    <Skeleton variant="text" />
                  </TableCell>
                </TableRow>
              ))
            ) : (
              data.map((row) => {
                const id = getRowId(row);
                const isItemSelected = isSelected(id);

                return (
                  <TableRow
                    hover
                    onClick={() => onRowClick?.(row)}
                    role={selectable ? 'checkbox' : undefined}
                    aria-checked={isItemSelected}
                    tabIndex={-1}
                    key={id}
                    selected={isItemSelected}
                    sx={{ cursor: onRowClick ? 'pointer' : 'default' }}
                  >
                    {selectable && (
                      <TableCell
                        padding="checkbox"
                        onClick={(e) => {
                          e.stopPropagation();
                          handleClick(row);
                        }}
                      >
                        <Checkbox
                          color="primary"
                          checked={isItemSelected}
                        />
                      </TableCell>
                    )}
                    {columns.map((column) => {
                      const value = row[column.id as keyof T];
                      return (
                        <TableCell key={column.id as string} align={column.align}>
                          {column.format ? column.format(value, row) : (value as React.ReactNode)}
                        </TableCell>
                      );
                    })}
                    <TableCell align="right">
                      <Box sx={{ display: 'flex', gap: 1, justifyContent: 'flex-end' }}>
                        {onRowClick && (
                          <IconButton
                            size="small"
                            onClick={(e) => {
                              e.stopPropagation();
                              onRowClick(row);
                            }}
                          >
                            <Visibility fontSize="small" />
                          </IconButton>
                        )}
                        {onEdit && (
                          <IconButton
                            size="small"
                            onClick={(e) => {
                              e.stopPropagation();
                              onEdit(row);
                            }}
                          >
                            <Edit fontSize="small" />
                          </IconButton>
                        )}
                        {onDelete && (
                          <IconButton
                            size="small"
                            onClick={(e) => {
                              e.stopPropagation();
                              onDelete(row);
                            }}
                          >
                            <Delete fontSize="small" />
                          </IconButton>
                        )}
                      </Box>
                    </TableCell>
                  </TableRow>
                );
              })
            )}
          </TableBody>
        </Table>
      </TableContainer>
      {(onPageChange || onRowsPerPageChange) && (
        <TablePagination
          rowsPerPageOptions={[5, 10, 25, 50]}
          component="div"
          count={totalCount || data.length}
          rowsPerPage={rowsPerPage}
          page={page}
          onPageChange={handleChangePage}
          onRowsPerPageChange={handleChangeRowsPerPage}
        />
      )}
    </Paper>
  );
}