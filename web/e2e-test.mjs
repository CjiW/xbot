import { chromium } from 'playwright';
const SCREENSHOT_DIR = '/tmp/web-debug-1000/screenshots';

(async () => {
  const browser = await chromium.launch({ headless: true });
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });

  try {
    // 1. Go to login page
    console.log('[1] Navigating to login page...');
    await page.goto('http://127.0.0.1:5199', { waitUntil: 'networkidle', timeout: 15000 });
    await page.screenshot({ path: `${SCREENSHOT_DIR}/01-login.png` });

    // 2. Login
    console.log('[2] Logging in...');
    await page.fill('input[name="username"]', 'smith');
    await page.fill('input[name="password"]', 'smith123');
    await page.click('button:has-text("登录")');
    await page.waitForTimeout(2000);
    await page.screenshot({ path: `${SCREENSHOT_DIR}/02-after-login.png` });

    // 3. Send a message
    console.log('[3] Sending test message...');
    const editor = page.locator('.tiptap');
    if (await editor.isVisible({ timeout: 5000 }).catch(() => false)) {
      await editor.click();
      await editor.fill('说一个1到10之间的随机数，只要数字');
    } else {
      // Fallback: use textarea
      const textarea = page.locator('textarea');
      await textarea.click();
      await textarea.fill('说一个1到10之间的随机数，只要数字');
    }

    // Click send button
    await page.click('button[aria-label="发送"], button.send-btn, button:has-text("发送")');
    console.log('[3] Message sent, waiting for response...');

    // 4. Wait for response (check for assistant message with content)
    console.log('[4] Waiting for assistant response...');
    // Wait up to 30 seconds for the response to complete
    let responseReceived = false;
    for (let i = 0; i < 30; i++) {
      await page.waitForTimeout(1000);
      // Check if there's an assistant message with actual content
      const assistantMsgs = await page.locator('.message-assistant, [data-role="assistant"]').count();
      if (assistantMsgs > 0) {
        // Check if the message has text content (not just loading)
        const hasContent = await page.evaluate(() => {
          const msgs = document.querySelectorAll('.message-assistant, [data-role="assistant"]');
          if (msgs.length === 0) return false;
          const last = msgs[msgs.length - 1];
          const text = last.textContent || '';
          return text.length > 5; // More than just "..."
        });
        if (hasContent) {
          responseReceived = true;
          console.log(`[4] Response received after ${i+1}s`);
          break;
        }
      }
      if (i % 5 === 4) console.log(`[4] Still waiting... ${i+1}s`);
    }

    await page.screenshot({ path: `${SCREENSHOT_DIR}/03-response.png` });

    // 5. Check iteration history in DOM
    console.log('[5] Checking iteration history...');
    const iterData = await page.evaluate(() => {
      // Try to find React state or DOM elements related to iterations
      const results = {};

      // Check for any thinking/reasoning blocks
      const thinkingBlocks = document.querySelectorAll('.thinking-block, .reasoning-block, [class*="thinking"], [class*="reasoning"]');
      results.thinkingBlockCount = thinkingBlocks.length;
      results.thinkingBlockHTML = Array.from(thinkingBlocks).map(el => el.outerHTML.substring(0, 200)).join('\n---\n');

      // Check for iteration indicators
      const iterElements = document.querySelectorAll('[class*="iteration"], [data-iteration]');
      results.iterationElementCount = iterElements.length;

      // Check for progress indicators
      const progressElements = document.querySelectorAll('[class*="progress"], [class*="live"]');
      results.progressElementCount = progressElements.length;

      // Get all message content
      const messages = document.querySelectorAll('.message-assistant, [data-role="assistant"], .chat-message');
      results.messageCount = messages.length;
      results.messageTexts = Array.from(messages).map(el => el.textContent?.substring(0, 200)).join('\n---\n');

      // Check React fiber for iterationHistory state
      const rootEl = document.getElementById('root');
      if (rootEl) {
        const fiberKey = Object.keys(rootEl).find(k => k.startsWith('__reactFiber'));
        if (fiberKey) {
          results.hasReactFiber = true;
        }
      }

      return results;
    });
    console.log('[5] Iteration check results:', JSON.stringify(iterData, null, 2));

    // 6. Wait a bit more for any delayed rendering, then take final screenshot
    await page.waitForTimeout(2000);
    await page.screenshot({ path: `${SCREENSHOT_DIR}/04-final.png`, fullPage: true });

    // 7. Full page screenshot to see everything
    console.log('[6] Taking full page screenshot...');
    await page.screenshot({ path: `${SCREENSHOT_DIR}/05-fullpage.png`, fullPage: true });

    // 8. Check console errors
    console.log('[7] Page errors during test:');
    page.on('pageerror', err => console.log('  Page error:', err.message));

    // Summary
    console.log('\n=== TEST SUMMARY ===');
    console.log('Response received:', responseReceived);
    console.log('Thinking blocks found:', iterData.thinkingBlockCount);
    console.log('Iteration elements:', iterData.iterationElementCount);
    console.log('Total messages:', iterData.messageCount);
    console.log('Message texts preview:', iterData.messageTexts?.substring(0, 300));

  } catch (err) {
    console.error('TEST ERROR:', err.message);
    await page.screenshot({ path: `${SCREENSHOT_DIR}/error.png` });
  } finally {
    await browser.close();
    console.log('\nDone. Screenshots saved to:', SCREENSHOT_DIR);
  }
})();
