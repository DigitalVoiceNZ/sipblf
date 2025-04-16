let currentSort = {
  column: document.querySelector('th.sortable.asc, th.sortable.desc')?.dataset.sort || 'extension',
  direction: document.querySelector('th.sortable.asc') ? 'asc' :
    document.querySelector('th.sortable.desc') ? 'desc' : 'asc'
};

function sortTable(column) {
  const table = document.getElementById('status-table');
  const tbody = table.querySelector('tbody');
  const rows = Array.from(tbody.querySelectorAll('tr'));
  const headers = table.querySelectorAll('th.sortable');

  // Update sort direction
  if (currentSort.column === column) {
    currentSort.direction = currentSort.direction === 'asc' ? 'desc' : 'asc';
  } else {
    currentSort = { column, direction: 'asc' };
  }

  // Update header indicators
  headers.forEach(header => {
    header.classList.remove('asc', 'desc');
    if (header.dataset.sort === column) {
      header.classList.add(currentSort.direction);
    }
  });

  // Sort rows
  rows.sort((a, b) => {
    let aVal, bVal;
    if (column === 'extension') {
      aVal = parseInt(a.querySelector('td').textContent, 10);
      bVal = parseInt(b.querySelector('td').textContent, 10);
    } else {
      aVal = a.querySelector('td:nth-child(2)').textContent.toLowerCase();
      bVal = b.querySelector('td:nth-child(2)').textContent.toLowerCase();
    }

    if (aVal < bVal) return currentSort.direction === 'asc' ? -1 : 1;
    if (aVal > bVal) return currentSort.direction === 'asc' ? 1 : -1;
    return 0;
  });

  // Reorder rows
  rows.forEach(row => tbody.appendChild(row));
}

// Add click handlers to sortable headers
document.querySelectorAll('th.sortable').forEach(header => {
  header.addEventListener('click', () => sortTable(header.dataset.sort));
});

let sse = null;
let reconnectTimeout = 1000; // Start with 1 second
const maxReconnectTimeout = 30000; // Max 30 seconds

function connectSSE() {
  if (sse !== null) {
    sse.close();
  }

  sse = new EventSource("/events");

  // Connection state handling
  sse.onopen = (e) => {
    console.log('SSE connection opened');
    reconnectTimeout = 1000; // Reset reconnect timeout on successful connection
  };

  sse.onerror = (e) => {
    console.error('SSE connection error');
    sse.close();

    // Exponential backoff for reconnection
    setTimeout(connectSSE, reconnectTimeout);

    // Increase reconnect timeout for next attempt, up to max
    reconnectTimeout = Math.min(reconnectTimeout * 2, maxReconnectTimeout);
  };

  // Reset reconnect timeout on any message
  sse.addEventListener('message', (e) => {
    // Reset reconnect timeout on successful message
    reconnectTimeout = 1000;

    // Process other messages
    processStateUpdate(e.data);
  });

  // Function to process state updates from SSE events
  function processStateUpdate(data) {
    const [extension, ...statusParts] = data.trim().split(' ');
    // Status may contain spaces (e.g. "Not in use"), so join remaining parts
    const status = statusParts.join(' ');

    console.log(`Status change: ${extension} → ${status}`);

    // Only process if we have both extension and status, and extension is numeric
    if (extension && status && /^\d+$/.test(extension)) {
      // This is a state update message
      // Convert status to display class and text
      let displayClass = "";

      switch (status.toLowerCase()) {
        case 'not in use':
          displayClass = ""; // Default state (green LED)
          break;
        case 'in use':
          displayClass = "in-use"; // Match CSS class name
          break;
        case 'ringing':
        case 'busy':
          displayClass = "ringing"; // Match CSS class name
          break;
        case 'unavailable':
        case 'unknown':
        default:
          displayClass = "disabled";
          break;
      }

      // Find or create the table row
      let row = document.getElementById("e-" + extension);
      if (!row) {
        // Create new row for this extension
        const tbody = document.querySelector('#status-table tbody');
        if (tbody) {
          row = document.createElement('tr');
          row.id = "e-" + extension;

          // Create extension cell
          const extCell = document.createElement('td');
          extCell.textContent = extension;
          row.appendChild(extCell);

          // Create description cell (empty for new extensions)
          const descCell = document.createElement('td');
          descCell.textContent = '';
          row.appendChild(descCell);

          // Create status cell (without LED indicator)
          const statusCell = document.createElement('td');
          statusCell.textContent = status;
          row.appendChild(statusCell);

          // Add device-state class to the extension cell for the LED indicator
          extCell.classList.add('device-state');

          // Insert the row in sorted order
          const rows = Array.from(tbody.querySelectorAll('tr'));
          const newExt = parseInt(extension, 10);
          let insertIndex = rows.findIndex(r => {
            const ext = parseInt(r.querySelector('td').textContent, 10);
            return ext > newExt;
          });

          if (insertIndex === -1) {
            tbody.appendChild(row); // Add at end
          } else {
            tbody.insertBefore(row, rows[insertIndex]);
          }
        }
      }

      // Update the row if we have it
      if (row) {
        // First remove any existing status classes
        row.classList.remove('in-use', 'ringing', 'busy', 'disabled');

        // Then add the new class if it's not empty
        if (displayClass) {
          row.classList.add(displayClass);
        }

        const statusCell = row.querySelector('td:nth-child(3)');
        if (statusCell) {
          statusCell.textContent = status;
        }
      }
    }
  }
}

// Function to test SSE connection by triggering a test update
function testSSEConnection(extension, status) {
  extension = extension || '99999';
  status = status || 'In use';

  console.log(`Testing SSE connection with ext=${extension}, status=${status}...`);

  // Make a request to the test endpoint
  fetch(`/test-update?ext=${encodeURIComponent(extension)}&state=${encodeURIComponent(status)}`)
    .then(response => response.text())
    .then(data => {
      console.log('Test update sent:', data);
      // Show a temporary success message
      const statusDiv = document.getElementById('sse-status');
      if (statusDiv) {
        const originalClass = statusDiv.className;
        const originalText = statusDiv.textContent;

        statusDiv.className = 'alert alert-info';
        statusDiv.textContent = `Test sent: ${extension} → ${status}`;

        // Restore original status after 3 seconds
        setTimeout(() => {
          statusDiv.className = originalClass;
          statusDiv.textContent = originalText;
        }, 3000);
      }
    })
    .catch(error => {
      console.error('Error sending test update:', error);
      // Show error message
      const statusDiv = document.getElementById('sse-status');
      if (statusDiv) {
        statusDiv.className = 'alert alert-danger';
        statusDiv.textContent = 'Test failed: ' + error.message;
      }
    });
}

// Start the SSE connection
connectSSE();
