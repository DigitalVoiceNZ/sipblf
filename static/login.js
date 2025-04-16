document.addEventListener('DOMContentLoaded', function() {
    const loginBtn = document.getElementById('loginBtn');
    const loginModal = new bootstrap.Modal(document.getElementById('loginModal'));
    const loginForm = document.getElementById('loginForm');
    const loginSubmit = document.getElementById('loginSubmit');
    const loginError = document.getElementById('loginError');
    let isAuthenticated = false;

    loginBtn.addEventListener('click', function() {
        loginModal.show();
    });

    loginSubmit.addEventListener('click', async function() {
        const password = document.getElementById('password').value;
        
        try {
            const response = await fetch('/api/login', {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                },
                body: JSON.stringify({ password: password })
            });

            if (response.ok) {
                isAuthenticated = true;
                loginModal.hide();
                document.getElementById('password').value = '';
                loginError.classList.add('d-none');
                // Refresh the page to show all extensions
                window.location.reload();
            } else {
                loginError.classList.remove('d-none');
            }
        } catch (error) {
            console.error('Login error:', error);
            loginError.classList.remove('d-none');
        }
    });

    // Handle form submission
    loginForm.addEventListener('submit', function(e) {
        e.preventDefault();
        loginSubmit.click();
    });
});
