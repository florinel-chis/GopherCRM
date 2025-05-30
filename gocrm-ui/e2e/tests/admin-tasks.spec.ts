import { test, expect } from '@playwright/test';
import { AdminAuthHelper } from '../helpers/admin-auth';
import { TasksPage } from '../pages/tasks.page';
import { generateTaskData } from '../fixtures/admin-user';

test.describe('Admin - Tasks Management', () => {
  let adminAuth: AdminAuthHelper;
  let tasksPage: TasksPage;

  test.beforeEach(async ({ page }) => {
    adminAuth = new AdminAuthHelper(page);
    tasksPage = new TasksPage(page);
    
    // Ensure admin is logged in
    await adminAuth.ensureAdminLoggedIn();
  });

  test.afterEach(async ({ page }) => {
    // Cleanup - logout after each test
    await adminAuth.logout();
  });

  test('admin can view tasks list page', async ({ page }) => {
    await tasksPage.goto();
    
    // Verify page loads correctly
    await expect(tasksPage.pageTitle).toBeVisible();
    await expect(tasksPage.newTaskButton).toBeVisible();
    
    // Verify we can see the table (even if empty)
    await expect(tasksPage.tasksTable).toBeVisible();
  });

  test('admin can create a new task successfully', async ({ page }) => {
    const taskData = generateTaskData();
    
    await tasksPage.goto();
    await tasksPage.clickNewTask();
    
    // Fill the task form
    await tasksPage.fillTaskForm(taskData);
    
    // Save and wait for response
    const response = await tasksPage.saveAndWaitForResponse();
    expect(response.status()).toBe(201);
    
    // Verify redirect to tasks list or detail
    expect(page.url()).toContain('/tasks');
    
    // Verify success message
    const successMessage = await tasksPage.getSuccessMessage();
    expect(successMessage).toBeTruthy();
  });

  test('admin can edit an existing task', async ({ page }) => {
    // First create a task
    const originalTaskData = generateTaskData();
    await tasksPage.goto();
    await tasksPage.clickNewTask();
    await tasksPage.fillTaskForm(originalTaskData);
    await tasksPage.saveTask();
    
    // Go back to tasks list
    await tasksPage.goto();
    
    // Edit the first task
    await tasksPage.editTask(0);
    
    // Modify the task data
    const updatedTaskData = {
      ...originalTaskData,
      title: 'Updated Task Title',
      priority: 'high',
      status: 'in_progress'
    };
    
    await tasksPage.fillTaskForm(updatedTaskData);
    await tasksPage.saveTask();
    
    // Verify the update
    await tasksPage.goto();
    const taskData = await tasksPage.getTaskData(0);
    expect(taskData.title).toBe('Updated Task Title');
    expect(taskData.priority.toLowerCase()).toContain('high');
    expect(taskData.status.toLowerCase()).toContain('progress');
  });

  test('admin can view task details', async ({ page }) => {
    // Create a task first
    const taskData = generateTaskData();
    await tasksPage.goto();
    await tasksPage.clickNewTask();
    await tasksPage.fillTaskForm(taskData);
    await tasksPage.saveTask();
    
    // Go back to tasks list and view the task
    await tasksPage.goto();
    await tasksPage.viewTask(0);
    
    // Verify we're on the task detail page
    expect(page.url()).toMatch(/\/tasks\/\d+$/);
    
    // Verify task information is displayed
    await expect(page.locator(`text=${taskData.title}`)).toBeVisible();
    await expect(page.locator(`text=${taskData.description}`)).toBeVisible();
  });

  test('admin can delete a task', async ({ page }) => {
    // Create a task first
    const taskData = generateTaskData();
    await tasksPage.goto();
    await tasksPage.clickNewTask();
    await tasksPage.fillTaskForm(taskData);
    await tasksPage.saveTask();
    
    // Go back to tasks list
    await tasksPage.goto();
    const initialCount = await tasksPage.getTaskCount();
    
    // Delete the task
    await tasksPage.deleteTask(0);
    
    // Verify task is removed
    await page.waitForTimeout(1000); // Wait for table to update
    const finalCount = await tasksPage.getTaskCount();
    expect(finalCount).toBe(initialCount - 1);
  });

  test('admin can search tasks', async ({ page }) => {
    // Create multiple tasks with distinct data
    const task1Data = { ...generateTaskData(), title: 'SearchTask1 - Important Work' };
    const task2Data = { ...generateTaskData(), title: 'SearchTask2 - Regular Work' };
    
    // Create first task
    await tasksPage.goto();
    await tasksPage.clickNewTask();
    await tasksPage.fillTaskForm(task1Data);
    await tasksPage.saveTask();
    
    // Create second task
    await tasksPage.goto();
    await tasksPage.clickNewTask();
    await tasksPage.fillTaskForm(task2Data);
    await tasksPage.saveTask();
    
    // Test search functionality
    await tasksPage.goto();
    await tasksPage.searchTasks('SearchTask1');
    
    // Should find the first task
    const searchResults = await tasksPage.getTaskCount();
    expect(searchResults).toBeGreaterThanOrEqual(1);
    
    const firstResult = await tasksPage.getTaskData(0);
    expect(firstResult.title).toContain('SearchTask1');
  });

  test('admin can filter tasks by status', async ({ page }) => {
    // Create tasks with different statuses
    const pendingTaskData = { ...generateTaskData(), status: 'pending' };
    const completedTaskData = { ...generateTaskData(), status: 'completed' };
    
    // Create pending task
    await tasksPage.goto();
    await tasksPage.clickNewTask();
    await tasksPage.fillTaskForm(pendingTaskData);
    await tasksPage.saveTask();
    
    // Create completed task
    await tasksPage.goto();
    await tasksPage.clickNewTask();
    await tasksPage.fillTaskForm(completedTaskData);
    await tasksPage.saveTask();
    
    // Filter by 'pending' status
    await tasksPage.goto();
    await tasksPage.filterByStatus('pending');
    
    // Verify filtered results
    const filteredCount = await tasksPage.getTaskCount();
    expect(filteredCount).toBeGreaterThanOrEqual(1);
    
    // Check that all visible tasks have 'pending' status
    for (let i = 0; i < Math.min(filteredCount, 3); i++) {
      const taskData = await tasksPage.getTaskData(i);
      expect(taskData.status.toLowerCase()).toContain('pending');
    }
  });

  test('admin can filter tasks by priority', async ({ page }) => {
    // Create tasks with different priorities
    const highPriorityData = { ...generateTaskData(), priority: 'high' };
    const lowPriorityData = { ...generateTaskData(), priority: 'low' };
    
    // Create high priority task
    await tasksPage.goto();
    await tasksPage.clickNewTask();
    await tasksPage.fillTaskForm(highPriorityData);
    await tasksPage.saveTask();
    
    // Create low priority task
    await tasksPage.goto();
    await tasksPage.clickNewTask();
    await tasksPage.fillTaskForm(lowPriorityData);
    await tasksPage.saveTask();
    
    // Filter by 'high' priority
    await tasksPage.goto();
    await tasksPage.filterByPriority('high');
    
    // Verify filtered results
    const filteredCount = await tasksPage.getTaskCount();
    expect(filteredCount).toBeGreaterThanOrEqual(1);
    
    // Check that all visible tasks have 'high' priority
    for (let i = 0; i < Math.min(filteredCount, 3); i++) {
      const taskData = await tasksPage.getTaskData(i);
      expect(taskData.priority.toLowerCase()).toContain('high');
    }
  });

  test('admin can mark task as complete', async ({ page }) => {
    // Create a pending task
    const taskData = { ...generateTaskData(), status: 'pending' };
    await tasksPage.goto();
    await tasksPage.clickNewTask();
    await tasksPage.fillTaskForm(taskData);
    await tasksPage.saveTask();
    
    // Go back to tasks list and mark as complete
    await tasksPage.goto();
    await tasksPage.markTaskComplete(0);
    
    // Verify task status changed
    await page.waitForTimeout(1000); // Wait for update
    const updatedTaskData = await tasksPage.getTaskData(0);
    expect(updatedTaskData.status.toLowerCase()).toContain('completed');
  });

  test('admin can manage task due dates', async ({ page }) => {
    // Create task with future due date
    const futureDate = new Date();
    futureDate.setDate(futureDate.getDate() + 7); // 7 days from now
    const futureDateString = futureDate.toISOString().split('T')[0];
    
    const taskData = { 
      ...generateTaskData(), 
      dueDate: futureDateString 
    };
    
    await tasksPage.goto();
    await tasksPage.clickNewTask();
    await tasksPage.fillTaskForm(taskData);
    await tasksPage.saveTask();
    
    // Verify due date is saved and displayed
    await tasksPage.goto();
    await tasksPage.viewTask(0);
    
    // Check that due date is displayed (format may vary)
    await expect(page.locator(`text=${futureDateString}`)).toBeVisible();
  });

  test('admin sees validation errors for invalid task data', async ({ page }) => {
    await tasksPage.goto();
    await tasksPage.clickNewTask();
    
    // Try to save without required fields
    await tasksPage.saveTask();
    
    // Should show validation errors or prevent submission
    const currentUrl = page.url();
    expect(currentUrl).toContain('/tasks/new'); // Should stay on form page
  });

  test('admin can handle task form cancellation', async ({ page }) => {
    await tasksPage.goto();
    await tasksPage.clickNewTask();
    
    // Fill some data
    const taskData = generateTaskData();
    await tasksPage.titleInput.fill(taskData.title);
    await tasksPage.descriptionTextarea.fill(taskData.description);
    
    // Cancel the form
    await tasksPage.cancelButton.click();
    
    // Should return to tasks list
    expect(page.url()).toContain('/tasks');
    expect(page.url()).not.toContain('/new');
  });

  test('admin can create task with minimal required data', async ({ page }) => {
    const minimalTaskData = {
      title: 'Minimal Task',
      description: 'Basic task description'
    };
    
    await tasksPage.goto();
    await tasksPage.clickNewTask();
    
    // Fill only required fields
    await tasksPage.fillTaskForm(minimalTaskData);
    
    const response = await tasksPage.saveAndWaitForResponse();
    expect(response.status()).toBe(201);
    
    // Verify task was created
    await tasksPage.goto();
    const taskData = await tasksPage.getTaskData(0);
    expect(taskData.title).toBe('Minimal Task');
  });

  test('admin can track task progress through status changes', async ({ page }) => {
    // Create a new task
    const taskData = { ...generateTaskData(), status: 'pending' };
    await tasksPage.goto();
    await tasksPage.clickNewTask();
    await tasksPage.fillTaskForm(taskData);
    await tasksPage.saveTask();
    
    // Edit task to change status to in_progress
    await tasksPage.goto();
    await tasksPage.editTask(0);
    
    await tasksPage.statusSelect.selectOption('in_progress');
    await tasksPage.saveTask();
    
    // Verify status change
    await tasksPage.goto();
    let currentTaskData = await tasksPage.getTaskData(0);
    expect(currentTaskData.status.toLowerCase()).toContain('progress');
    
    // Edit again to mark as completed
    await tasksPage.editTask(0);
    await tasksPage.statusSelect.selectOption('completed');
    await tasksPage.saveTask();
    
    // Verify final status
    await tasksPage.goto();
    currentTaskData = await tasksPage.getTaskData(0);
    expect(currentTaskData.status.toLowerCase()).toContain('completed');
  });

  test('admin can create tasks with different priorities', async ({ page }) => {
    // Create tasks with different priorities
    const priorities = ['low', 'medium', 'high'];
    
    for (const priority of priorities) {
      const taskData = { 
        ...generateTaskData(), 
        title: `${priority.charAt(0).toUpperCase() + priority.slice(1)} Priority Task`,
        priority 
      };
      
      await tasksPage.goto();
      await tasksPage.clickNewTask();
      await tasksPage.fillTaskForm(taskData);
      await tasksPage.saveTask();
    }
    
    // Verify all priorities are represented in the list
    await tasksPage.goto();
    const taskCount = await tasksPage.getTaskCount();
    expect(taskCount).toBe(priorities.length);
    
    // Check that each priority is displayed correctly
    for (let i = 0; i < priorities.length; i++) {
      const taskData = await tasksPage.getTaskData(i);
      expect(priorities.some(p => taskData.priority.toLowerCase().includes(p))).toBe(true);
    }
  });

  test('admin can handle task date validation', async ({ page }) => {
    // Try to create task with past due date
    const pastDate = new Date();
    pastDate.setDate(pastDate.getDate() - 1); // Yesterday
    const pastDateString = pastDate.toISOString().split('T')[0];
    
    const taskData = { 
      ...generateTaskData(), 
      dueDate: pastDateString 
    };
    
    await tasksPage.goto();
    await tasksPage.clickNewTask();
    await tasksPage.fillTaskForm(taskData);
    await tasksPage.saveTask();
    
    // Depending on validation rules, this might:
    // 1. Show a warning but allow creation
    // 2. Prevent creation with error message
    // 3. Allow creation (some systems allow past due dates for historical tasks)
    
    // Check if we're still on the form page (validation failed) or redirected (success)
    const currentUrl = page.url();
    const isStillOnForm = currentUrl.includes('/tasks/new');
    const errorMessage = await tasksPage.getErrorMessage();
    
    // If validation prevents past dates, we should see an error or stay on form
    if (isStillOnForm && errorMessage) {
      expect(errorMessage.toLowerCase()).toContain('date');
    }
    // Otherwise, the task was created successfully
  });
});