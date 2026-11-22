import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent, within } from '@testing-library/react';
import { DataTable } from './DataTable';
import type { Column } from './DataTable';
import { RowActionsMenu } from './RowActionsMenu';

interface TestData {
  id: number;
  name: string;
  email: string;
  role: string;
}

const mockData: TestData[] = [
  { id: 1, name: 'John Doe', email: 'john@example.com', role: 'Admin' },
  { id: 2, name: 'Jane Smith', email: 'jane@example.com', role: 'User' },
  { id: 3, name: 'Bob Johnson', email: 'bob@example.com', role: 'User' },
];

const columns: Column<TestData>[] = [
  { id: 'name', label: 'Name' },
  { id: 'email', label: 'Email' },
  { id: 'role', label: 'Role' },
];

describe('DataTable', () => {
  it('renders table with data', () => {
    render(<DataTable columns={columns} data={mockData} />);
    
    // Check headers
    expect(screen.getByText('Name')).toBeInTheDocument();
    expect(screen.getByText('Email')).toBeInTheDocument();
    expect(screen.getByText('Role')).toBeInTheDocument();
    
    // Check data
    expect(screen.getByText('John Doe')).toBeInTheDocument();
    expect(screen.getByText('jane@example.com')).toBeInTheDocument();
    expect(screen.getAllByText('User')).toHaveLength(2); // Two users have 'User' role
  });

  it('renders with title', () => {
    render(<DataTable columns={columns} data={mockData} title="Users Table" />);
    expect(screen.getByText('Users Table')).toBeInTheDocument();
  });

  it('shows loading state', () => {
    const { container } = render(<DataTable columns={columns} data={[]} loading={true} rowsPerPage={5} />);
    const skeletons = container.querySelectorAll('.MuiSkeleton-root');
    expect(skeletons.length).toBeGreaterThan(0);
  });

  it('handles row click', () => {
    const handleRowClick = vi.fn();
    render(<DataTable columns={columns} data={mockData} onRowClick={handleRowClick} />);
    
    fireEvent.click(screen.getByText('John Doe'));
    expect(handleRowClick).toHaveBeenCalledWith(mockData[0]);
  });

  it('handles pagination', () => {
    const handlePageChange = vi.fn();
    render(
      <DataTable
        columns={columns}
        data={mockData}
        page={0}
        rowsPerPage={2}
        totalCount={3}
        onPageChange={handlePageChange}
      />
    );
    
    const nextButton = screen.getByRole('button', { name: /next page/i });
    fireEvent.click(nextButton);
    expect(handlePageChange).toHaveBeenCalledWith(1);
  });

  it('handles selection when selectable', () => {
    const handleSelectionChange = vi.fn();
    const { container } = render(
      <DataTable
        columns={columns}
        data={mockData}
        selectable={true}
        onSelectionChange={handleSelectionChange}
      />
    );
    
    const checkboxes = container.querySelectorAll('input[type="checkbox"]');
    fireEvent.click(checkboxes[1]); // Click first data row checkbox
    
    expect(handleSelectionChange).toHaveBeenCalledWith([1]);
  });

  it('handles search', () => {
    const handleSearch = vi.fn();
    render(<DataTable columns={columns} data={mockData} onSearch={handleSearch} />);
    
    const searchInput = screen.getByPlaceholderText('Search...');
    fireEvent.change(searchInput, { target: { value: 'test' } });
    
    expect(handleSearch).toHaveBeenCalledWith('test');
  });

  describe('sorting', () => {
    it('toggles asc -> desc on repeat clicks while uncontrolled', () => {
      const handleSort = vi.fn();
      render(<DataTable columns={columns} data={mockData} onSort={handleSort} />);

      const nameHeader = screen.getByRole('button', { name: /name/i });
      fireEvent.click(nameHeader);
      expect(handleSort).toHaveBeenLastCalledWith('name', 'asc');

      fireEvent.click(nameHeader);
      expect(handleSort).toHaveBeenLastCalledWith('name', 'desc');
    });

    it('renders the sort indicator from the controlled props', () => {
      const { rerender, container } = render(
        <DataTable
          columns={columns}
          data={mockData}
          onSort={vi.fn()}
          sortBy="email"
          sortOrder="desc"
        />
      );

      const headerCell = () =>
        container.querySelector('th:nth-of-type(2)') as HTMLElement;
      expect(headerCell()).toHaveAttribute('aria-sort', 'descending');

      rerender(
        <DataTable
          columns={columns}
          data={mockData}
          onSort={vi.fn()}
          sortBy="email"
          sortOrder="asc"
        />
      );
      expect(headerCell()).toHaveAttribute('aria-sort', 'ascending');
    });

    // The defect this guards: a sort click changes the list page's query key,
    // which sends the page down its loading branch and unmounts the table. Any
    // sort state kept inside DataTable is destroyed, so the next click on the
    // same column recomputes 'asc' and 'desc' is unreachable. With the parent
    // owning the state, a remount keeps the direction.
    it('reaches desc after a remount when the parent owns the sort state', () => {
      const handleSort = vi.fn();
      const { unmount } = render(
        <DataTable
          columns={columns}
          data={mockData}
          onSort={handleSort}
          sortBy=""
          sortOrder="asc"
        />
      );

      fireEvent.click(screen.getByRole('button', { name: /name/i }));
      expect(handleSort).toHaveBeenLastCalledWith('name', 'asc');

      // Stand in for the loading branch swapping the table out and back in.
      unmount();
      render(
        <DataTable
          columns={columns}
          data={mockData}
          onSort={handleSort}
          sortBy="name"
          sortOrder="asc"
        />
      );

      fireEvent.click(screen.getByRole('button', { name: /name/i }));
      expect(handleSort).toHaveBeenLastCalledWith('name', 'desc');
    });

    it('does not move its own indicator while controlled', () => {
      const handleSort = vi.fn();
      const { container } = render(
        <DataTable
          columns={columns}
          data={mockData}
          onSort={handleSort}
          sortBy=""
          sortOrder="asc"
        />
      );

      fireEvent.click(screen.getByRole('button', { name: /name/i }));

      expect(handleSort).toHaveBeenCalledWith('name', 'asc');
      // The parent has not fed the new value back yet, so nothing is marked.
      expect(container.querySelector('th[aria-sort]')).toBeNull();
    });
  });

  it('formats column values when format function is provided', () => {
    const columnsWithFormat: Column<TestData>[] = [
      { id: 'name', label: 'Name' },
      {
        id: 'email',
        label: 'Email',
        format: (value) => value.toString().toUpperCase(),
      },
      { id: 'role', label: 'Role' },
    ];
    
    render(<DataTable columns={columnsWithFormat} data={mockData} />);
    expect(screen.getByText('JOHN@EXAMPLE.COM')).toBeInTheDocument();
  });
});
describe('DataTable rowActions', () => {
  it('renders the per-row controls with their own row and keeps their clicks off the row', async () => {
    const onRowClick = vi.fn();
    const onAction = vi.fn();
    render(
      <DataTable
        columns={columns}
        data={mockData}
        onRowClick={onRowClick}
        rowActions={(row) => (
          <RowActionsMenu
            label={`More actions for ${row.name}`}
            actions={[{ label: 'Ping', onClick: () => onAction(row.id) }]}
          />
        )}
      />
    );

    fireEvent.click(screen.getByRole('button', { name: 'More actions for Jane Smith' }));
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Ping' }));

    expect(onAction).toHaveBeenCalledWith(2);
    expect(onRowClick).not.toHaveBeenCalled();
  });

  it('renders nothing for a row without actions', () => {
    render(
      <DataTable
        columns={columns}
        data={mockData}
        rowActions={(row) => (
          <RowActionsMenu label={`More actions for ${row.name}`} actions={[]} />
        )}
      />
    );

    expect(screen.queryByRole('button', { name: /^More actions for / })).not.toBeInTheDocument();
  });

  // The list pages unmount the table while a sort or search loads, so their
  // tests cannot catch a binding to the index a row had when the table first
  // mounted. Reorder the rows of one mounted table instead.
  it('binds row controls to the row record when the same mounted table is reordered', async () => {
    const onAction = vi.fn();
    const onEdit = vi.fn();
    const rowActions = (row: TestData) => (
      <RowActionsMenu
        label={`More actions for ${row.name}`}
        actions={[{ label: 'Ping', onClick: () => onAction(row.id) }]}
      />
    );
    const { rerender } = render(
      <DataTable columns={columns} data={mockData} onEdit={onEdit} rowActions={rowActions} />
    );

    rerender(
      <DataTable
        columns={columns}
        data={[...mockData].reverse()}
        onEdit={onEdit}
        rowActions={rowActions}
      />
    );
    expect(screen.getAllByRole('row')[1]).toHaveTextContent('Bob Johnson');

    // John Doe moved from the first row to the last.
    const row = screen.getByText('John Doe').closest('tr');
    if (!row) throw new Error('no row for John Doe');
    const trigger = within(row).getByRole('button', { name: /^More actions for / });
    expect(trigger).toHaveAccessibleName('More actions for John Doe');
    fireEvent.click(trigger);
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Ping' }));
    expect(onAction).toHaveBeenCalledWith(1);

    fireEvent.click(within(row).getByTestId('EditIcon'));
    expect(onEdit).toHaveBeenCalledWith(mockData[0]);
  });
});

describe('DataTable row action names', () => {
  const rowOf = (name: string): HTMLElement => {
    const row = screen.getByText(name).closest('tr');
    if (!row) throw new Error(`no row for ${name}`);
    return row;
  };

  it('names the view, edit and delete buttons with the bare verbs without getRowLabel', () => {
    render(
      <DataTable
        columns={columns}
        data={mockData}
        onRowClick={vi.fn()}
        onEdit={vi.fn()}
        onDelete={vi.fn()}
      />
    );
    const row = within(rowOf('John Doe'));
    expect(row.getByRole('button', { name: 'View' })).toHaveAccessibleName('View');
    expect(row.getByRole('button', { name: 'Edit' })).toHaveAccessibleName('Edit');
    expect(row.getByRole('button', { name: 'Delete' })).toHaveAccessibleName('Delete');
  });

  it('adds the row label to the button names with getRowLabel', () => {
    render(
      <DataTable
        columns={columns}
        data={mockData}
        onRowClick={vi.fn()}
        onEdit={vi.fn()}
        onDelete={vi.fn()}
        getRowLabel={(r) => r.name}
      />
    );
    const row = within(rowOf('John Doe'));
    expect(row.getByRole('button', { name: 'View John Doe' })).toBeInTheDocument();
    expect(row.getByRole('button', { name: 'Edit John Doe' })).toBeInTheDocument();
    expect(row.getByRole('button', { name: 'Delete John Doe' })).toBeInTheDocument();
    expect(row.queryByRole('button', { name: 'View' })).not.toBeInTheDocument();
    expect(row.queryByRole('button', { name: 'Edit' })).not.toBeInTheDocument();
    expect(row.queryByRole('button', { name: 'Delete' })).not.toBeInTheDocument();
  });

  it('falls back to the bare verbs when the label is blank', () => {
    render(
      <DataTable columns={columns} data={mockData} onEdit={vi.fn()} getRowLabel={() => '   '} />
    );
    const row = within(rowOf('John Doe'));
    expect(row.getByRole('button', { name: 'Edit' })).toHaveAccessibleName('Edit');
  });

  it('calls each named button its own handler once with that row', () => {
    const onRowClick = vi.fn();
    const onEdit = vi.fn();
    const onDelete = vi.fn();
    render(
      <DataTable
        columns={columns}
        data={mockData}
        onRowClick={onRowClick}
        onEdit={onEdit}
        onDelete={onDelete}
        getRowLabel={(r) => r.name}
      />
    );
    const row = within(rowOf('Jane Smith'));

    fireEvent.click(row.getByRole('button', { name: 'Edit Jane Smith' }));
    expect(onEdit).toHaveBeenCalledTimes(1);
    expect(onEdit).toHaveBeenCalledWith(mockData[1]);
    expect(onRowClick).not.toHaveBeenCalled();

    fireEvent.click(row.getByRole('button', { name: 'Delete Jane Smith' }));
    expect(onDelete).toHaveBeenCalledTimes(1);
    expect(onDelete).toHaveBeenCalledWith(mockData[1]);
    expect(onRowClick).not.toHaveBeenCalled();

    fireEvent.click(row.getByRole('button', { name: 'View Jane Smith' }));
    expect(onRowClick).toHaveBeenCalledTimes(1);
    expect(onRowClick).toHaveBeenCalledWith(mockData[1]);
    expect(onEdit).toHaveBeenCalledTimes(1);
    expect(onDelete).toHaveBeenCalledTimes(1);
  });
});
