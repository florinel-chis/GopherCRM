import React from 'react';
import { useNavigate } from 'react-router-dom';
import { ToggleButton, ToggleButtonGroup } from '@mui/material';
import { ViewKanban as BoardIcon, ViewList as ListIcon } from '@mui/icons-material';
import { writeDealView, type DealView } from './dealView';

export interface DealViewToggleProps {
  value: DealView;
}

/** "List" / "Board" switch shared by the two deals pages; remembers the choice. */
export const DealViewToggle: React.FC<DealViewToggleProps> = ({ value }) => {
  const navigate = useNavigate();

  const handleChange = (_event: React.MouseEvent<HTMLElement>, next: DealView | null) => {
    if (next === null || next === value) {
      return;
    }
    writeDealView(next);
    navigate(next === 'board' ? '/deals/board' : '/deals');
  };

  return (
    <ToggleButtonGroup
      value={value}
      exclusive
      size="small"
      onChange={handleChange}
      aria-label="Deals view"
    >
      <ToggleButton value="list" aria-label="List">
        <ListIcon fontSize="small" sx={{ mr: 0.5 }} />
        List
      </ToggleButton>
      <ToggleButton value="board" aria-label="Board">
        <BoardIcon fontSize="small" sx={{ mr: 0.5 }} />
        Board
      </ToggleButton>
    </ToggleButtonGroup>
  );
};
