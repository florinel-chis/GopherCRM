import React, { useId, useState } from 'react';
import { IconButton, ListItemIcon, ListItemText, Menu, MenuItem } from '@mui/material';
import { MoreVert as MoreVertIcon } from '@mui/icons-material';

export interface RowAction {
  label: string;
  onClick: () => void;
  icon?: React.ReactNode;
  disabled?: boolean;
}

interface RowActionsMenuProps {
  /** Accessible name of the trigger, naming the row: "More actions for Acme". */
  label: string;
  actions: RowAction[];
}

/**
 * Overflow menu for one table row. Every instance owns its own open state and
 * closes over the actions built for its row, so an action always targets the
 * row whose button was pressed — whatever the current sort, filter or page.
 * Renders nothing when the row has no action for the current user.
 */
export const RowActionsMenu: React.FC<RowActionsMenuProps> = ({ label, actions }) => {
  const [anchorEl, setAnchorEl] = useState<HTMLElement | null>(null);
  const buttonId = useId();
  const menuId = useId();

  if (actions.length === 0) {
    return null;
  }

  const open = Boolean(anchorEl);

  return (
    <>
      <IconButton
        id={buttonId}
        size="small"
        aria-label={label}
        aria-haspopup="menu"
        aria-controls={open ? menuId : undefined}
        aria-expanded={open ? 'true' : undefined}
        onClick={(event) => setAnchorEl(event.currentTarget)}
      >
        <MoreVertIcon fontSize="small" />
      </IconButton>
      <Menu
        id={menuId}
        anchorEl={anchorEl}
        open={open}
        onClose={() => setAnchorEl(null)}
        MenuListProps={{ 'aria-labelledby': buttonId }}
      >
        {actions.map((action) => (
          <MenuItem
            key={action.label}
            disabled={action.disabled}
            onClick={() => {
              if (action.disabled) return;
              setAnchorEl(null);
              action.onClick();
            }}
          >
            {action.icon && <ListItemIcon>{action.icon}</ListItemIcon>}
            <ListItemText>{action.label}</ListItemText>
          </MenuItem>
        ))}
      </Menu>
    </>
  );
};
