import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import { DataTable } from './DataTable';
import type { Column } from './DataTable';

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