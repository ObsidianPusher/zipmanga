(() => {
  const form = document.getElementById('loginForm');
  const errorEl = document.getElementById('loginError');
  const loginURL = new URL('api/login', document.baseURI).toString();
  const homeURL = new URL('./', document.baseURI).toString();

  async function onSubmit(event) {
    event.preventDefault();
    errorEl.hidden = true;

    const username = document.getElementById('username').value.trim();
    const password = document.getElementById('password').value;

    const resp = await fetch(loginURL, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username, password })
    });

    if (!resp.ok) {
      let msg = '登录失败';
      try {
        const data = await resp.json();
        if (data.error) msg = data.error;
      } catch (_) {}
      errorEl.textContent = msg;
      errorEl.hidden = false;
      return;
    }

    location.href = homeURL;
  }

  form.addEventListener('submit', onSubmit);
})();
