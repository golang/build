/*
  Copyright 2022 The Go Authors. All rights reserved.
  Use of this source code is governed by a BSD-style
  license that can be found in the LICENSE file.
*/

;(function () {
  const registerTaskListExpandListeners = (selector) => {
    document.querySelectorAll(selector).forEach((element) => {
      element.addEventListener("click", (e) => {
        e.stopPropagation()
        element.classList.toggle("TaskList-expanded")
        element.nextElementSibling.classList.toggle("TaskList-expanded")
      })
    })
  }

  registerTaskListExpandListeners(".TaskList-expandableItem")
})()
